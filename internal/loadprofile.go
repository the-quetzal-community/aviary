package internal

import (
	"fmt"
	"os"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// loadprofile.go instruments the cold-start / loading path so we can see what
// dominates the time between process launch and the loading splash being
// dismissed. It is intentionally self-contained and cheap: a session loads
// exactly once, so the bookkeeping cost is irrelevant.
//
// Opt-in: run with AVIARY_LOADPROF=1 to enable. The timeline marks go to
// stderr as they happen and a summary report is printed once, from
// finishLoading. Off by default so normal runs are unaffected (zero overhead).

var loadProfileOn = os.Getenv("AVIARY_LOADPROF") == "1"

// loadProgramStart is stamped at package-init time — as close to process start
// as we can get from Go — so every timeline mark is relative to launch.
var loadProgramStart = time.Now()

func sinceStartMs() float64 {
	return float64(time.Since(loadProgramStart).Microseconds()) / 1000
}

// cpuProfileFile holds the open CPU-profile output while a load is being
// profiled, so finishLoading can stop and close it.
var cpuProfileFile *os.File

// startLoadCPUProfile begins a runtime/pprof CPU profile spanning the load when
// AVIARY_PPROF=<path> is set. Unlike the hand-rolled buckets, this samples every
// goroutine's stack, so it attributes time to the real culprit (append/growslice,
// GC mark, runtime.cgocall, …) instead of whichever Go frame happened to be on
// the main thread. Stopped in finishLoading. No-op when the env var is unset.
func startLoadCPUProfile() {
	path := os.Getenv("AVIARY_PPROF")
	if path == "" {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		profMark("pprof: create %s failed: %v", path, err)
		return
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		profMark("pprof: start failed: %v", err)
		f.Close()
		return
	}
	cpuProfileFile = f
	profMark("pprof: CPU profile started -> %s", path)
}

// stopLoadCPUProfile ends the CPU profile started by startLoadCPUProfile.
func stopLoadCPUProfile() {
	if cpuProfileFile == nil {
		return
	}
	pprof.StopCPUProfile()
	cpuProfileFile.Close()
	cpuProfileFile = nil
	profMark("pprof: CPU profile stopped")
}

// profMark logs a one-off timeline event with a timestamp relative to launch.
// Safe to call from any goroutine.
func profMark(format string, args ...any) {
	if !loadProfileOn {
		return
	}
	fmt.Fprintf(os.Stderr, "[loadprof %9.1fms] %s\n", sinceStartMs(), fmt.Sprintf(format, args...))
}

// ProfMark is the exported entry point for package main (which can't reach the
// unexported helper).
func ProfMark(format string, args ...any) { profMark(format, args...) }

// profBucket accumulates total wall time and a call count for a category of
// work. All access is via atomics so the queue-drain closures (main thread),
// LoadSync (main + loader thread) and the decode goroutine can all feed it.
type profBucket struct {
	nanos atomic.Int64
	count atomic.Int64
}

func (b *profBucket) add(d time.Duration) {
	b.nanos.Add(int64(d))
	b.count.Add(1)
}

func (b *profBucket) ms() float64  { return float64(b.nanos.Load()) / 1e6 }
func (b *profBucket) calls() int64 { return b.count.Load() }

// Per-category buckets. The first five are the queue-drain mutation closures
// (main-thread time spent applying the replayed .mus3 log). bucketLoadSync is
// every synchronous resource load (a subset of Import + the Ready() loads).
// bucketStorageOpen is the storage.Open call (cloud round-trips on the join /
// together path).
var (
	bucketSculpt      profBucket
	bucketImport      profBucket
	bucketChange      profBucket
	bucketAction      profBucket
	bucketLookAt      profBucket
	bucketLoadSync    profBucket
	bucketStorageOpen profBucket

	// bucketDecodeRead is time the decode goroutine spent in the underlying
	// file/cloud Read (network + disk IO during replay). bucketEnqueueBlock is
	// time the decode goroutine spent blocked on a full world.queue — i.e.
	// waiting for the main thread to make room. A large EnqueueBlock means the
	// main-thread apply is the bottleneck (raising loadDrainBudget would help);
	// near-zero means the decode/stream is the bottleneck (budget is irrelevant).
	bucketDecodeRead   profBucket
	bucketEnqueueBlock profBucket

	// Terrain paint-texture cost split: bucketTexLoad is LoadSync of the sibling
	// (_norm/_spec) textures (serialised through the single loader thread);
	// bucketTexDecode is GetImage/Decompress/Convert (CPU, per-image, the part
	// that could parallelise across cores); bucketTexArray is the Texture2DArray
	// assembly.
	bucketTexLoad   profBucket
	bucketTexDecode profBucket
	bucketTexArray  profBucket

	// bucketDressing is time spent in scatterGrass/eraseGrass (the dressing
	// apply) during the replay — the suspected grass cost.
	bucketDressing profBucket

	// Splits of the Sculpt closure: editor.Sculpt vs the per-stroke
	// ui.Editor.Sculpt call (design-explorer bookkeeping run on EVERY sculpt).
	bucketEditorSculpt profBucket
	bucketUISculpt     profBucket

	// Critter editor sub-paths (the dominant editor by sculpt count).
	bucketCritterBone   profBucket
	bucketCritterLeg    profBucket
	bucketCritterSlider profBucket

	// Terrain height/paint sub-paths.
	bucketTerrainCapture profBucket // captureObjectHeights (O(objects) per height stroke)
	bucketTilesIntersect profBucket // tilesIntersecting (creates tiles via generateBase)
	bucketTerrainTiles   profBucket // the tile.Sculpt loop only
	bucketUploadDesign   profBucket // uploadDesign inside tile.Sculpt (bulk)
	bucketTileReload     profBucket // tile.Reload inside tile.Sculpt (bulk)
	bucketGenerateBase   profBucket // generateBase: lazy first-touch tile mesh/collision build
	bucketGenBaseWaste   profBucket // the sub-part of generateBase redone at flush (surface upload, tangents, sides, water)
)

// editorSculptCounts tallies sculpts per brush.Editor key so we can see which
// editor's strokes dominate the replay.
var (
	editorSculptMu     sync.Mutex
	editorSculptCounts = map[string]int{}
)

func countEditorSculpt(editor string) {
	if !loadProfileOn {
		return
	}
	editorSculptMu.Lock()
	editorSculptCounts[editor]++
	editorSculptMu.Unlock()
}

// Per-frame drain-loop accounting (set in processLoading). framesBudgetHit
// counts frames that still had queued work at the 30ms deadline (apply-bound
// that frame); framesQueueEmpty counts frames that drained the queue to empty
// and yielded early (not apply-bound). maxQueueDepth is the deepest backlog
// seen at a frame start.
var (
	loadFramesBudgetHit  atomic.Int64
	loadFramesQueueEmpty atomic.Int64
	loadMaxQueueDepth    atomic.Int64
	loadDecodeStartUs    atomic.Int64
	loadDecodeEndUs      atomic.Int64
)

// markMaxQueueDepth records d if it is a new high-water mark.
func markMaxQueueDepth(d int64) {
	if !loadProfileOn {
		return
	}
	for {
		cur := loadMaxQueueDepth.Load()
		if d <= cur || loadMaxQueueDepth.CompareAndSwap(cur, d) {
			return
		}
	}
}

// timeIn starts a timer and returns a closure that, when called, adds the
// elapsed time to b. Intended for `defer timeIn(&bucket)()`.
func timeIn(b *profBucket) func() {
	if !loadProfileOn {
		return func() {}
	}
	start := time.Now()
	return func() { b.add(time.Since(start)) }
}

// Per-path load accounting, so the report can name the slowest individual
// resources (a single huge .glb, a slow-compiling shader, a network-fetched
// library asset, …).
var (
	loadPathMu    sync.Mutex
	loadPathStats = map[string]*pathStat{}
)

type pathStat struct {
	nanos int64
	count int64
}

// recordLoad attributes one resource load to bucketLoadSync and to its path.
func recordLoad(path string, d time.Duration) {
	if !loadProfileOn {
		return
	}
	bucketLoadSync.add(d)
	loadPathMu.Lock()
	s := loadPathStats[path]
	if s == nil {
		s = &pathStat{}
		loadPathStats[path] = s
	}
	s.nanos += int64(d)
	s.count++
	loadPathMu.Unlock()
}

// loadPathMs returns the total LoadSync time recorded for a path, 0 if none.
func loadPathMs(path string) float64 {
	if !loadProfileOn {
		return 0
	}
	loadPathMu.Lock()
	defer loadPathMu.Unlock()
	if s := loadPathStats[path]; s != nil {
		return float64(s.nanos) / 1e6
	}
	return 0
}

var loadReportOnce sync.Once

// reportLoadProfile prints the timing summary. Called from finishLoading once
// the world is fully built and the splash comes down.
func reportLoadProfile(enqueued, dequeued int64) {
	if !loadProfileOn {
		return
	}
	loadReportOnce.Do(func() {
		var b []byte
		out := func(format string, args ...any) {
			b = append(b, fmt.Sprintf(format, args...)...)
		}
		out("\n================ LOAD PROFILE ================\n")
		out("total wall time to dismiss splash: %.1f ms\n", sinceStartMs())
		out("mutations: %d enqueued, %d applied\n\n", enqueued, dequeued)

		out("main-thread time applying replay (by mutation kind):\n")
		type kindRow struct {
			name string
			b    *profBucket
		}
		kinds := []kindRow{
			{"Import (load+instantiate)", &bucketImport},
			{"Change (instantiate/move)", &bucketChange},
			{"Sculpt (terrain regen)", &bucketSculpt},
			{"Action", &bucketAction},
			{"LookAt (avatars)", &bucketLookAt},
		}
		sort.Slice(kinds, func(i, j int) bool {
			return kinds[i].b.nanos.Load() > kinds[j].b.nanos.Load()
		})
		for _, k := range kinds {
			if k.b.calls() == 0 {
				continue
			}
			out("  %-28s %9.1f ms  (%d calls, %.2f ms/call avg)\n",
				k.name, k.b.ms(), k.b.calls(), k.b.ms()/float64(k.b.calls()))
		}

		out("\nstorage.Open (open save, incl. cloud fetch): %.1f ms (%d opens)\n",
			bucketStorageOpen.ms(), bucketStorageOpen.calls())
		out("all LoadSync resource loads: %.1f ms across %d loads\n",
			bucketLoadSync.ms(), bucketLoadSync.calls())
		out("terrain paint textures: load %.1fms (%d) | DECODE %.1fms (%d, parallelisable) | arrays %.1fms (%d)\n",
			bucketTexLoad.ms(), bucketTexLoad.calls(),
			bucketTexDecode.ms(), bucketTexDecode.calls(),
			bucketTexArray.ms(), bucketTexArray.calls())
		out("dressing (scatter+erase grass): %.1f ms across %d strokes\n",
			bucketDressing.ms(), bucketDressing.calls())
		out("Sculpt split: editor.Sculpt %.1fms (%d) | ui.Editor.Sculpt %.1fms (%d)\n",
			bucketEditorSculpt.ms(), bucketEditorSculpt.calls(),
			bucketUISculpt.ms(), bucketUISculpt.calls())
		out("critter sub-paths: bone %.1fms (%d) | leg %.1fms (%d) | slider %.1fms (%d)\n",
			bucketCritterBone.ms(), bucketCritterBone.calls(),
			bucketCritterLeg.ms(), bucketCritterLeg.calls(),
			bucketCritterSlider.ms(), bucketCritterSlider.calls())
		out("terrain height/paint: capture %.1fms (%d) | tilesIntersecting %.1fms (%d) | tile.Sculpt loop %.1fms (%d)\n",
			bucketTerrainCapture.ms(), bucketTerrainCapture.calls(),
			bucketTilesIntersect.ms(), bucketTilesIntersect.calls(),
			bucketTerrainTiles.ms(), bucketTerrainTiles.calls())
		out("  tile.Sculpt internals: uploadDesign %.1fms (%d) | tile.Reload %.1fms (%d)\n",
			bucketUploadDesign.ms(), bucketUploadDesign.calls(),
			bucketTileReload.ms(), bucketTileReload.calls())
		out("  generateBase (lazy first-touch): %.1fms (%d) | of which redone-at-flush: %.1fms (%d)\n",
			bucketGenerateBase.ms(), bucketGenerateBase.calls(),
			bucketGenBaseWaste.ms(), bucketGenBaseWaste.calls())
		editorSculptMu.Lock()
		out("sculpts by brush.Editor: ")
		for k, v := range editorSculptCounts {
			label := k
			if label == "" {
				label = "(terrain/legacy)"
			}
			out("%s=%d ", label, v)
		}
		editorSculptMu.Unlock()
		out("\n")

		// Decode goroutine: where its wall time went, and the bottleneck verdict.
		decodeWallMs := float64(loadDecodeEndUs.Load()-loadDecodeStartUs.Load()) / 1000
		readMs := bucketDecodeRead.ms()
		blockMs := bucketEnqueueBlock.ms()
		parseMs := decodeWallMs - readMs - blockMs
		out("\ndecode goroutine wall: %.1f ms\n", decodeWallMs)
		out("  file/cloud Read (IO+network): %.1f ms (%d reads)\n", readMs, bucketDecodeRead.calls())
		out("  blocked on full queue (apply too slow): %.1f ms\n", blockMs)
		out("  parse/dispatch (remainder): %.1f ms\n", parseMs)

		out("\ndrain budget = %v\n", loadDrainBudget)
		hit := loadFramesBudgetHit.Load()
		empty := loadFramesQueueEmpty.Load()
		out("  frames apply-bound (hit budget w/ backlog): %d\n", hit)
		out("  frames not apply-bound (drained to empty):  %d\n", empty)
		out("  max queue backlog at a frame start: %d (queue cap 1000)\n", loadMaxQueueDepth.Load())
		verdict := "DECODE-bound — raising loadDrainBudget will NOT help"
		if blockMs > 500 || (hit > empty && hit > 0) {
			verdict = "APPLY-bound — raising loadDrainBudget should help"
		}
		out("  verdict: %s\n", verdict)

		// Top slowest individual resource paths by total time.
		loadPathMu.Lock()
		type row struct {
			path  string
			nanos int64
			count int64
		}
		rows := make([]row, 0, len(loadPathStats))
		for p, s := range loadPathStats {
			rows = append(rows, row{p, s.nanos, s.count})
		}
		loadPathMu.Unlock()
		// Categorise all LoadSync time by file kind so we can see whether texture
		// decompression dominates the blocking loads (the parallelisation target).
		cat := map[string]*pathStat{}
		bump := func(k string, n, c int64) {
			s := cat[k]
			if s == nil {
				s = &pathStat{}
				cat[k] = s
			}
			s.nanos += n
			s.count += c
		}
		for _, r := range rows {
			lp := strings.ToLower(r.path)
			switch {
			case strings.HasSuffix(lp, ".png"), strings.HasSuffix(lp, ".jpg"), strings.HasSuffix(lp, ".jpeg"),
				strings.HasSuffix(lp, ".webp"), strings.HasSuffix(lp, ".exr"), strings.HasSuffix(lp, ".svg"):
				bump("texture", r.nanos, r.count)
			case strings.HasSuffix(lp, ".glb"), strings.HasSuffix(lp, ".gltf"), strings.HasSuffix(lp, ".scn"), strings.HasSuffix(lp, ".obj"):
				bump("scene", r.nanos, r.count)
			case strings.HasSuffix(lp, ".tres"), strings.HasSuffix(lp, ".material"), strings.HasSuffix(lp, ".res"):
				bump("material/res", r.nanos, r.count)
			default:
				bump("other", r.nanos, r.count)
			}
		}
		out("\nLoadSync time by kind:\n")
		for _, k := range []string{"texture", "scene", "material/res", "other"} {
			if s := cat[k]; s != nil {
				out("  %-14s %8.1f ms across %d loads\n", k, float64(s.nanos)/1e6, s.count)
			}
		}
		// Roll texture loads up by source dir (first 3 path segments) to see where
		// the bulk of the texture-decompression time is concentrated.
		src := map[string]*pathStat{}
		for _, r := range rows {
			lp := strings.ToLower(r.path)
			if !(strings.HasSuffix(lp, ".png") || strings.HasSuffix(lp, ".jpg") || strings.HasSuffix(lp, ".jpeg") ||
				strings.HasSuffix(lp, ".webp") || strings.HasSuffix(lp, ".exr") || strings.HasSuffix(lp, ".svg")) {
				continue
			}
			p := strings.TrimPrefix(r.path, "res://")
			seg := strings.Split(p, "/")
			key := p
			if len(seg) >= 3 {
				key = strings.Join(seg[:3], "/")
			} else if len(seg) >= 1 {
				key = seg[0]
			}
			s := src[key]
			if s == nil {
				s = &pathStat{}
				src[key] = s
			}
			s.nanos += r.nanos
			s.count += r.count
		}
		srcRows := make([]row, 0, len(src))
		for k, s := range src {
			srcRows = append(srcRows, row{k, s.nanos, s.count})
		}
		sort.Slice(srcRows, func(i, j int) bool { return srcRows[i].nanos > srcRows[j].nanos })
		out("\ntexture loads by source dir:\n")
		for i, r := range srcRows {
			if i >= 15 {
				out("  ... and %d more\n", len(srcRows)-15)
				break
			}
			out("  %8.1f ms  x%-4d  %s\n", float64(r.nanos)/1e6, r.count, r.path)
		}

		sort.Slice(rows, func(i, j int) bool { return rows[i].nanos > rows[j].nanos })
		out("\nslowest resource loads (total time, may repeat):\n")
		for i, r := range rows {
			if i >= 20 {
				out("  ... and %d more paths\n", len(rows)-20)
				break
			}
			out("  %9.1f ms  x%-3d  %s\n", float64(r.nanos)/1e6, r.count, r.path)
		}
		out("=============================================\n")
		os.Stderr.Write(b)
	})
}
