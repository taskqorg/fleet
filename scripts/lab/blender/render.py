# Renders FLEET_INPUT_DIR/measure/model.glb to $FLEET_OUTPUT_DIR/renders/<view>.png with Eevee
# (docs/designs/fleet-asset-pipelines.md §4). Args: --views front,side,top,three-quarter --size 1024 [--glb PATH].
import math, os, sys
import bpy
from mathutils import Vector

args = sys.argv[sys.argv.index('--') + 1:] if '--' in sys.argv else []
opt = lambda name, default: args[args.index(name) + 1] if name in args else default
views = opt('--views', 'front,side,top,three-quarter').split(',')
size = int(opt('--size', '1024'))
glb = opt('--glb', os.path.join(os.environ.get('FLEET_INPUT_DIR', ''), 'measure', 'model.glb'))
out = os.path.join(os.environ['FLEET_OUTPUT_DIR'], 'renders')
os.makedirs(out, exist_ok=True)

bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=glb)
scene = bpy.context.scene
meshes = [o for o in scene.objects if o.type == 'MESH']
corners = [o.matrix_world @ Vector(c) for o in meshes for c in o.bound_box]
center = sum(corners, Vector()) / len(corners)
radius = max((c - center).length for c in corners)

engines = [e.identifier for e in bpy.types.RenderSettings.bl_rna.properties['engine'].enum_items]
scene.render.engine = 'BLENDER_EEVEE_NEXT' if 'BLENDER_EEVEE_NEXT' in engines else 'BLENDER_EEVEE'
scene.render.resolution_x = scene.render.resolution_y = size
scene.render.film_transparent = True
world = bpy.data.worlds.new('lab')
world.use_nodes = True
world.node_tree.nodes['Background'].inputs['Strength'].default_value = 1.0
scene.world = world
sun = bpy.data.objects.new('sun', bpy.data.lights.new('sun', 'SUN'))
sun.data.energy = 3
sun.rotation_euler = (math.radians(50), 0, math.radians(30))
scene.collection.objects.link(sun)
camera = bpy.data.objects.new('camera', bpy.data.cameras.new('camera'))
camera.data.type = 'ORTHO'
camera.data.ortho_scale = radius * 2.2
scene.collection.objects.link(camera)
scene.camera = camera

# glTF is Y-up, imported as Blender Z-up: front looks along +Y.
directions = {'front': (0, -1, 0), 'side': (1, 0, 0), 'top': (0, 0, 1), 'three-quarter': (1, -1, 0.8)}
for view in views:
    d = Vector(directions[view]).normalized()
    camera.location = center + d * radius * 4
    camera.rotation_euler = (center - camera.location).to_track_quat('-Z', 'Y').to_euler()
    scene.render.filepath = os.path.join(out, view + '.png')
    bpy.ops.render.render(write_still=True)
