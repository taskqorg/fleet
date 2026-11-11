# Renders FLEET_INPUT_DIR/measure/model.glb to $FLEET_OUTPUT_DIR/renders/<view>.png with Eevee
# (docs/designs/fleet-asset-pipelines.md §4). Args: --views front,side,top,three-quarter --size 1024 [--glb PATH].
import hashlib, json, math, os, re, sys
import bpy
from mathutils import Matrix, Vector

args = sys.argv[sys.argv.index('--') + 1:] if '--' in sys.argv else []
opt = lambda name, default: args[args.index(name) + 1] if name in args else default
views = opt('--views', 'front,side,top,three-quarter').split(',')
size = int(opt('--size', '1024'))
glb = opt('--glb', os.path.join(os.environ.get('FLEET_INPUT_DIR', ''), 'measure', 'model.glb'))
out = os.path.join(os.environ['FLEET_OUTPUT_DIR'], 'renders')
os.makedirs(out, exist_ok=True)

blend = opt('--blend', '')
if blend:
    bpy.ops.wm.open_mainfile(filepath=blend)
else:
    bpy.ops.wm.read_factory_settings(use_empty=True)
    bpy.ops.import_scene.gltf(filepath=glb)
scene = bpy.context.scene
meshes = [o for o in scene.objects if o.type == 'MESH' and not o.hide_render]
if not meshes: raise ValueError('no visible mesh to render')
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
directions = {'front': (0, -1, 0), 'side': (1, 0, 0), 'left': (1, 0, 0), 'right': (-1, 0, 0), 'back': (0, 1, 0), 'top': (0, 0, 1), 'three-quarter': (1, -1, 0.8)}
if opt('--front-axis', '-Y') == '+X':
    directions.update(front=(1,0,0),left=(0,1,0),right=(0,-1,0),back=(-1,0,0),side=(0,1,0),**{'three-quarter':(1,1,.8)})
def render_set(folder):
    deps=bpy.context.evaluated_depsgraph_get()
    corners=[o.evaluated_get(deps).matrix_world@Vector(c) for o in meshes for c in o.evaluated_get(deps).bound_box]
    center=sum(corners,Vector())/len(corners);radius=max((c-center).length for c in corners)
    if radius<=0:raise ValueError('render bounds unavailable')
    camera.data.ortho_scale=radius*2.2
    os.makedirs(folder,exist_ok=True)
    result=[]
    for view in views:
        d=Vector(directions[view]).normalized();camera.location=center+d*radius*4
        camera.rotation_euler=(center-camera.location).to_track_quat('-Z','Y').to_euler()
        scene.render.filepath=os.path.join(folder,view+'.png');bpy.ops.render.render(write_still=True)
        with open(scene.render.filepath,'rb') as f: data=f.read()
        result.append({'view':view,'path':os.path.relpath(scene.render.filepath,os.environ['FLEET_OUTPUT_DIR']),'sha256':hashlib.sha256(data).hexdigest(),'width':size,'height':size})
    return result

rendered={'rest':render_set(out)}
if '--poses' in args:
    if not blend:raise ValueError('pose rendering requires the retained rig blend')
    guide_path=opt('--guide',os.path.join(os.environ.get('FLEET_INPUT_DIR',''),'example','rig-guide.json'))
    with open(guide_path) as f:guide=json.load(f)
    arms=[o for o in scene.objects if o.type=='ARMATURE']
    if len(arms)!=1:raise ValueError('one retained character armature required')
    arm=arms[0];names=guide['bones'];parents=guide['parents']
    if set(names)!=set(arm.data.bones.keys()):raise ValueError('pose guide differs from retained rig')
    def matrix(values):return Matrix([values[:4],values[4:8],values[8:12],[0,0,0,1]])
    for pose in guide['poses']:
        if not re.fullmatch(r'[a-z][a-z0-9_]*',pose['name']):raise ValueError('invalid pose artifact name')
        if len(pose['world'])!=len(names)*12:raise ValueError('pose matrix count differs from guide')
        world=[matrix(pose['world'][i*12:i*12+12]) for i in range(len(names))]
        for i,name in enumerate(names):
            parent=parents[i];rest=arm.data.bones[name].matrix_local
            local=world[i]
            if parent>=0:
                rest=arm.data.bones[names[parent]].matrix_local.inverted()@rest
                local=world[parent].inverted()@local
            arm.pose.bones[name].matrix_basis=rest.inverted()@local
        bpy.context.view_layer.update()
        rendered[pose['name']]=render_set(os.path.join(out,'poses',pose['name']))
    with open(os.path.join(os.environ['FLEET_OUTPUT_DIR'],'output.json'),'w') as f:
        json.dump({'renders':rendered,'coverage':guide['coverage'],'original_seven_pose_acceptance':'unavailable'},f)
