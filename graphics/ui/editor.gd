@tool
extends Control

const tabs: Array[String] = [
	"foliage",
	"mineral",
	"terrain",
	"shelter",
	"citizen",
	"critter",
]

# Called when the node enters the scene tree for the first time.
func _ready() -> void:
	var library = DirAccess.open("res://library")
	if !library:
		return
	
	
	var i: int = 0
	for tab in tabs:
		var hlayout = HBoxContainer.new()
		hlayout.name = tab
		var texture = load("res://ui/"+tab+".svg")
		if texture:
			$Editor.add_child(hlayout)
			$Editor.set_tab_icon(i, texture)
			$Editor.set_tab_title(i, "")
			i = i + 1
