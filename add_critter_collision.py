#!/usr/bin/env python3
"""Point the skinned critter models at the shared convex-collision post-import
script.

The everything/critter/*.glb are skinned, so the AviaryModelLoader GLTF extension
can't give them collision (it can't reach the mesh nested under the importer's
generated Skeleton3D). Instead we run graphics/import_collision.gd as an
EditorScenePostImport step, which adds convex-decomposition collision after the
importer bakes nodes/root_scale — so it is correctly scaled, and convex (valid on
the moving critter bodies, unlike a static-only trimesh).

The .glb.import files are not git-tracked, so run this whenever critters are
(re)imported. A reimport is required for the change to take effect (build.sh /
`gd run` will do it once the .import files differ).
"""
import glob
import os
import re

CRITTER_GLOB = "graphics/library/everything/critter/*.glb.import"
SCRIPT_PATH = "res://import_collision.gd"

here = os.path.dirname(os.path.abspath(__file__))
for f in sorted(glob.glob(os.path.join(here, CRITTER_GLOB))):
    text = open(f).read()
    if "import_script/path=" not in text:
        print("!! no import_script/path line in", f, "- skipped")
        continue
    new = re.sub(r'import_script/path=".*?"', f'import_script/path="{SCRIPT_PATH}"', text)
    if new != text:
        open(f, "w").write(new)
        print("patched", os.path.relpath(f, here))
    else:
        print("already set", os.path.relpath(f, here))
