package internal

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"graphics.gd/classdb/BaseButton"
	"graphics.gd/classdb/Button"
	"graphics.gd/classdb/Control"
	"graphics.gd/classdb/DirAccess"
	"graphics.gd/classdb/Engine"
	"graphics.gd/classdb/GridContainer"
	"graphics.gd/classdb/Image"
	"graphics.gd/classdb/ImageTexture"
	"graphics.gd/classdb/Node"
	"graphics.gd/classdb/Panel"
	"graphics.gd/classdb/SceneTree"
	"graphics.gd/classdb/ScrollContainer"
	"graphics.gd/classdb/TextEdit"
	"graphics.gd/classdb/Texture2D"
	"graphics.gd/classdb/TextureButton"
	"graphics.gd/variant/Callable"
	"graphics.gd/variant/Object"
	"graphics.gd/variant/Vector2"
	"the.quetzal.community/aviary/internal/musical"
	"the.quetzal.community/aviary/internal/networking"
)

type FlightPlanner struct {
	Panel.Extension[FlightPlanner] `gd:"FlightPlanner"`

	Back TextureButton.Instance `gd:"%Back"`
	Keys GridContainer.Instance `gd:"%Keys"`
	Code TextEdit.Instance      `gd:"%Code"`
	Plus Button.Instance        `gd:"%Plus"`

	Maps GridContainer.Instance `gd:"%Maps"`

	// Avatar switcher (built programmatically in Ready, no scene nodes):
	// AvatarButton sits under the dialpad showing the player's current avatar;
	// clicking it swaps the right-hand maps area (mapsPanel) for avatarPanel, a
	// design-explorer-style grid of avatar previews. Picking one updates the
	// client's broadcast avatar design and restores the maps view.
	AvatarButton  TextureButton.Instance
	Avatars       GridContainer.Instance
	avatarPanel   ScrollContainer.Instance
	mapsPanel     Control.Instance
	avatarsBuilt  bool
	switcherReady bool

	client      *Client
	clientReady sync.WaitGroup

	processed  map[string]struct{}
	on_process chan func()
}

func (fl *FlightPlanner) Reload() {
	for i, child := range fl.Maps.AsNode().GetChildren() {
		if i > 0 {
			child.QueueFree()
		}
	}
	fl.Maps.SetColumns(int(fl.AsControl().Size().X/256) - 1)
	DirAccess.MakeDirAbsolute("user://snaps")
	for save := range DirAccess.Open("user://snaps").Iter() {
		if strings.HasSuffix(save, ".png") {
			fl.processed[save] = struct{}{}
			fl.Maps.AsNode().AddChild(TextureButton.New().
				SetIgnoreTextureSize(true).
				SetStretchMode(TextureButton.StretchKeepAspect).
				AsTextureButton().SetTextureNormal(ImageTexture.CreateFromImage(Image.LoadFromFile("user://snaps/" + save)).AsTexture2D()).
				AsBaseButton().OnPressed(
				func() {
					record, err := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(save, ".png"))
					if err != nil {
						Engine.Raise(err)
						return
					}
					fl.replaceTree(NewClientLoading(musical.WorkID(record)))
				}).
				AsControl().SetCustomMinimumSize(Vector2.New(256, 256)).AsNode(),
			)
		}
	}
	if fl.switcherReady {
		// Re-opening the planner always starts on the maps view, never a
		// half-open avatar picker left from a previous session.
		fl.showMaps()
	}
	go fl.fetchCloudSnaps()
}

// avatarPreviewURI maps an avatar library URI (res://library/.../x.glb) to its
// baked preview thumbnail (res://preview/.../x.glb.png).
func avatarPreviewURI(libraryURI string) string {
	return strings.Replace(libraryURI, "res://library/", "res://preview/", 1) + ".png"
}

const (
	avatarLibraryDir = "res://library/everything/avatar"
	avatarPreviewDir = "res://preview/everything/avatar"
)

// buildAvatarSwitcher creates the avatar preview button (under the dialpad) and
// the hidden right-hand picker panel. Called once from Ready; the picker grid
// itself is filled lazily on first open (buildAvatars).
func (fl *FlightPlanner) buildAvatarSwitcher() {
	// Preview button, slotted into the dialpad's Column VBox right under Keys.
	column := fl.Keys.AsNode().GetParent()
	fl.AvatarButton = TextureButton.New().
		SetIgnoreTextureSize(true).
		SetStretchMode(TextureButton.StretchKeepAspectCentered)
	fl.AvatarButton.SetTextureNormal(LoadSync[Texture2D.Instance](avatarPreviewURI(defaultAvatarURI)))
	fl.AvatarButton.AsControl().SetCustomMinimumSize(Vector2.New(0, 140))
	fl.AvatarButton.AsControl().SetSizeFlagsHorizontal(Control.SizeExpandFill)
	fl.AvatarButton.AsControl().SetTooltipText("Choose your avatar")
	fl.AvatarButton.AsBaseButton().OnPressed(fl.toggleAvatarPicker)
	column.AddChild(fl.AvatarButton.AsNode())

	// Right-hand picker panel: a scrollable grid added to the Layout HBox,
	// toggled against the existing maps area (the GridFlowContainer).
	fl.mapsPanel = Object.To[Control.Instance](fl.AsNode().GetNode("Layout/GridFlowContainer"))
	fl.avatarPanel = ScrollContainer.New()
	fl.avatarPanel.AsControl().SetSizeFlagsHorizontal(Control.SizeExpandFill)
	fl.avatarPanel.AsControl().SetSizeFlagsVertical(Control.SizeExpandFill)
	fl.Avatars = GridContainer.New()
	fl.Avatars.AsControl().SetSizeFlagsHorizontal(Control.SizeExpandFill)
	fl.avatarPanel.AsNode().AddChild(fl.Avatars.AsNode())
	fl.AsNode().GetNode("Layout").AddChild(fl.avatarPanel.AsNode())
	fl.avatarPanel.AsCanvasItem().SetVisible(false)
	fl.switcherReady = true
}

// showMaps restores the saved-games view (and hides the avatar picker).
func (fl *FlightPlanner) showMaps() {
	fl.avatarPanel.AsCanvasItem().SetVisible(false)
	fl.mapsPanel.AsCanvasItem().SetVisible(true)
}

// toggleAvatarPicker swaps between the saved-games maps and the avatar picker.
func (fl *FlightPlanner) toggleAvatarPicker() {
	if fl.avatarPanel.AsCanvasItem().Visible() {
		fl.showMaps()
		return
	}
	fl.buildAvatars()
	fl.mapsPanel.AsCanvasItem().SetVisible(false)
	fl.avatarPanel.AsCanvasItem().SetVisible(true)
}

// buildAvatars fills the picker grid from the baked avatar previews, once.
func (fl *FlightPlanner) buildAvatars() {
	if fl.avatarsBuilt {
		return
	}
	fl.avatarsBuilt = true
	fl.Avatars.SetColumns(max(1, int(fl.AsControl().Size().X/256)-1))
	dir := DirAccess.Open(avatarPreviewDir)
	if dir == DirAccess.Nil {
		return
	}
	for name := range dir.Iter() {
		name = strings.TrimSuffix(name, ".import")
		if !strings.HasSuffix(name, ".png") || strings.HasSuffix(name, "_cut.glb.png") {
			continue
		}
		previewPath := avatarPreviewDir + "/" + name
		resource := avatarLibraryDir + "/" + strings.TrimSuffix(name, ".png")
		tile := TextureButton.New().
			SetIgnoreTextureSize(true).
			SetStretchMode(TextureButton.StretchKeepAspectCentered)
		tileID := tile.ID()
		// Thumbnails load off the main thread (they stream from library.pck);
		// the tile shows immediately and its texture pops in when ready.
		LoadAsync(previewPath, func(tex Texture2D.Instance) {
			if tex == Texture2D.Nil {
				return
			}
			if b, ok := tileID.Instance(); ok {
				b.SetTextureNormal(tex)
			}
		})
		tile.AsBaseButton().OnPressed(func() {
			fl.pickAvatar(resource, previewPath)
		})
		fl.Avatars.AsNode().AddChild(tile.
			AsControl().SetCustomMinimumSize(Vector2.New(256, 256)).AsNode())
	}
}

// pickAvatar records the chosen avatar on the client (broadcast in the next
// LookAt — see Client.Process), updates the preview button, and returns to the
// maps view. avatar is reset to zero so Process re-registers the new design.
func (fl *FlightPlanner) pickAvatar(resource, previewPath string) {
	fl.client.avatarResource = resource
	fl.client.avatar = musical.Design{}
	fl.AvatarButton.SetTextureNormal(LoadSync[Texture2D.Instance](previewPath))
	fl.showMaps()
}

func (fl *FlightPlanner) fetchCloudSnaps() {
	fl.clientReady.Wait()
	fl.client.clientReady.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	saves, err := fl.client.signalling.CloudSaves(ctx)
	if err != nil {
		Engine.Raise(err)
		return
	}
	for _, save := range saves {
		if _, ok := fl.processed[string(save)+".png"]; ok {
			continue
		}
		stat, err := os.Stat(UserDataDir + "/snaps/" + string(save) + ".png")
		if err == nil && stat.Size() > 0 {
			continue
		}
		snap, err := fl.client.signalling.LookupSnap(ctx, save)
		if err != nil {
			Engine.Raise(err)
			continue
		}
		buf, err := io.ReadAll(snap)
		if err != nil {
			Engine.Raise(err)
			continue
		}
		Callable.Defer(Callable.New(func() {
			var image = Image.New()
			image.LoadPngFromBuffer(buf)
			image.SavePng("user://snaps/" + string(save) + ".png")
			mapButton := Object.Leak(TextureButton.New()).
				SetIgnoreTextureSize(true).
				SetStretchMode(TextureButton.StretchKeepAspect).
				AsTextureButton().SetTextureNormal(ImageTexture.CreateFromImage(image).AsTexture2D()).
				AsBaseButton().OnPressed(
				func() {
					record, err := base64.RawURLEncoding.DecodeString(string(save))
					if err != nil {
						Engine.Raise(err)
						return
					}
					fl.replaceTree(NewClientLoading(musical.WorkID(record)))
				}).
				AsControl().SetCustomMinimumSize(Vector2.New(256, 256))
			select {
			case fl.on_process <- func() {
				fl.Maps.AsNode().AddChild(mapButton.AsNode())
				Object.Free(mapButton)
			}:
			default:
				Object.Free(mapButton)
			}
		}))
	}
}

func (fl *FlightPlanner) replaceTree(fresh *Client) {
	replaceSceneTree(fl.AsNode(), fresh)
}

// replaceSceneTree tears down every root child and swaps in a fresh
// Client. Shared by the flight planner's load/join/new-save buttons
// and the cloud login's session-swap path.
func replaceSceneTree(anchor Node.Instance, fresh *Client) {
	for _, child := range SceneTree.Get(anchor).Root().AsNode().GetChildren() {
		child.QueueFree()
	}
	SceneTree.Add(fresh)
}

func (fl *FlightPlanner) Ready() {
	fl.clientReady.Add(1)
	fl.on_process = make(chan func(), 10)
	fl.processed = make(map[string]struct{})
	fl.buildAvatarSwitcher()
	fl.Reload()
	fl.Code.SetText("")
	fl.Back.AsBaseButton().OnPressed(func() {
		fl.AsCanvasItem().SetVisible(false)
	})
	fl.Plus.AsBaseButton().OnPressed(func() {
		var record musical.WorkID
		if _, err := rand.Read(record[:]); err != nil {
			Engine.Raise(err)
			return
		}
		fl.replaceTree(NewClientLoading(record))
	})
	fl.Code.OnTextChanged(func() {
		text := fl.Code.Text()
		safe := ""
		for _, char := range text {
			if strings.ContainsRune("0123456789", char) {
				safe += string(char)
			}
		}
		if len(safe) > 6 {
			safe = safe[:6]
		}
		if text != safe {
			fl.Code.SetText(safe)
		}
	})
	keys := fl.Keys.AsNode().GetChildren()
	for _, key := range keys {
		name := key.Name()
		switch name {
		case "X":
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				text := fl.Code.Text()
				if len(text) > 0 {
					text = text[:len(text)-1]
				}
				fl.Code.SetText(text)
			})
		case ">":
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				fresh := NewClientJoining()
				fl.replaceTree(fresh)
				go fresh.apiJoin(networking.Code(fl.Code.Text()))
			})
		default:
			Object.To[BaseButton.Instance](key).OnPressed(func() {
				text := fl.Code.Text()
				text += name
				fl.Code.SetText(text)
			})
		}
	}
}
