package internal

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"graphics.gd/classdb/DisplayServer"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/Viewport"
	"graphics.gd/classdb/ViewportTexture"
	"graphics.gd/variant/Float"
)

// devShot is the scripted offline harness: Aviary's in-engine test suite for
// everything that only exists inside the engine (internal cannot host plain Go
// tests that link it). It activates when AVIARY_DEMO and/or AVIARY_SHOT is set.
//
//   - AVIARY_DEMO=<scenario> runs a scenario from devDemos once the world has
//     finished loading, then quits. A scenario is a list of steps that drive the
//     real mutation paths (publishChange, commitSculpt, Undo/Redo) and log what
//     they measured; a broken expectation logs "devshot <name>: ERROR ...".
//   - AVIARY_SHOT=<path.png> saves the rendered viewport at the end (and at each
//     snap step, as <path>-<label>.png). With no demo it waits ~1.5s of frames,
//     takes the shot and quits. AVIARY_SHOTPATH is the older name, still read.
//   - AVIARY_DEMO_TIMEOUT=<seconds> bounds the scenario (default 60): a step that
//     never finishes is an ERROR, not a hang.
//
// Under --headless the dummy renderer has no frame to grab, so snaps are skipped
// and the log is the whole result — see tools/devshot.sh, which runs every
// scenario that way against a scratch profile, several at a time.
//
// The harness prints "devshot state:" as it quits; that line is what tells the
// runner the scenario reached its end (the exit code can't: the engine may still
// fail on the way out, long after every measurement was logged).
type devShot struct {
	name     string
	path     string
	headless bool
	timeout  Float.X
	steps    []devStep

	elapsed Float.X // since the world finished loading
	stage   int     // index of the running step
	frames  int     // frames spent in the running step
	stepT   Float.X // seconds spent in the running step
	failed  bool
	done    bool
}

// devStep is one step of a scenario. It is called every frame until it reports
// true, then the next step starts. Steps that just act return true at once;
// steps that wait on the world (a mutation to apply) return the condition.
type devStep func(s *devShot, w *Client) bool

func newDevShot() *devShot {
	name := os.Getenv("AVIARY_DEMO")
	path := os.Getenv("AVIARY_SHOT")
	if path == "" {
		path = os.Getenv("AVIARY_SHOTPATH")
	}
	if name == "" && path == "" {
		return nil
	}
	s := &devShot{name: name, path: path, headless: DisplayServer.GetName() == "headless", timeout: 60}
	if v := os.Getenv("AVIARY_DEMO_TIMEOUT"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			s.timeout = Float.X(f)
		}
	}
	switch {
	case name == "":
		s.name = "shot"
		s.steps = []devStep{waitFrames(90)} // ~1.5s of rendered frames so the world settles
	case devDemos[name] != nil:
		s.steps = devDemos[name]()
	default:
		s.steps = []devStep{func(s *devShot, w *Client) bool {
			s.errorf("no such scenario (see devDemos in internal/devdemos.go)")
			return true
		}}
	}
	return s
}

func (s *devShot) logf(format string, args ...any) {
	fmt.Printf("devshot %s: %s\n", s.name, fmt.Sprintf(format, args...))
}

// errorf records a failed expectation; the scenario keeps going so one run
// reports every broken thing, not just the first.
func (s *devShot) errorf(format string, args ...any) {
	s.failed = true
	s.logf("ERROR "+format, args...)
}

// check logs an ERROR unless ok, and returns ok.
func (s *devShot) check(ok bool, format string, args ...any) bool {
	if !ok {
		s.errorf(format, args...)
	}
	return ok
}

// process runs the scenario one frame. Called from Client.Process once the
// world has finished loading.
func (s *devShot) process(w *Client, dt Float.X) {
	if s == nil || s.done {
		return
	}
	if s.elapsed == 0 {
		s.logf("begin (%d steps)", len(s.steps))
	}
	s.elapsed += dt
	if s.stage < len(s.steps) {
		if s.steps[s.stage](s, w) {
			s.stage++
			s.frames, s.stepT = 0, 0
		} else {
			s.frames++
			s.stepT += dt
		}
	}
	if s.stage < len(s.steps) && s.elapsed < s.timeout {
		return
	}
	if s.stage < len(s.steps) {
		s.errorf("timed out after %.0fs at step %d of %d", float64(s.timeout), s.stage+1, len(s.steps))
	}
	s.end(w)
}

func (s *devShot) end(w *Client) {
	s.done = true
	if s.path != "" {
		s.save(w, s.path)
	}
	w.debugResourceUsage()
	if w.TerrainEditor != nil {
		w.TerrainEditor.debugTerrainSignature()
	}
	if w.CritterEditor != nil {
		w.CritterEditor.debugCritterSignature()
	}
	s.logf("state: failed=%v steps=%d/%d editing=%v entities=%d elapsed=%.1fs",
		s.failed, s.stage, len(s.steps), w.Editing, len(w.entity_to_object), float64(s.elapsed))
	fmt.Printf("devshot state: %s failed=%v\n", s.name, s.failed)
	SceneTree.Get(w.AsNode()).Quit()
}

// save writes the rendered viewport to path. Headless runs have nothing to
// grab, so they skip it (the log is the result there).
func (s *devShot) save(w *Client, path string) {
	if s.headless {
		return
	}
	tex := Viewport.Get(w.AsNode()).GetTexture()
	if tex == (ViewportTexture.Instance{}) {
		s.errorf("screenshot: nil viewport texture")
		return
	}
	img := tex.AsTexture2D().GetImage()
	if img == (Image.Instance{}) {
		s.errorf("screenshot: nil image")
		return
	}
	if err := img.SavePng(path); err != nil {
		s.errorf("screenshot save failed: %v", err)
		return
	}
	s.logf("screenshot saved to %s", path)
}

// waitFrames lets the world run n frames (mutations apply, tweens settle).
func waitFrames(n int) devStep {
	return func(s *devShot, w *Client) bool { return s.frames >= n }
}

// until waits for cond; if the scenario times out on it, what names the wait.
func until(what string, cond func(w *Client) bool) devStep {
	return func(s *devShot, w *Client) bool {
		if cond(w) {
			s.logf("%s (%d frames)", what, s.frames)
			return true
		}
		return false
	}
}

// do runs fn once.
func do(fn func(s *devShot, w *Client)) devStep {
	return func(s *devShot, w *Client) bool { fn(s, w); return true }
}

// snap saves a labelled screenshot beside AVIARY_SHOT (<path>-<label>.png),
// after letting a few frames render so the change is on screen.
func snap(label string) devStep {
	return func(s *devShot, w *Client) bool {
		if s.frames < 10 {
			return false
		}
		if s.path != "" {
			s.save(w, strings.TrimSuffix(s.path, ".png")+"-"+label+".png")
		}
		return true
	}
}
