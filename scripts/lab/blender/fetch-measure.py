# Downloads the GLB named in FLEET_INPUT_DIR/gen/output.json (model_urls.glb) to $FLEET_OUTPUT_DIR/model.glb, imports it
# and writes $FLEET_OUTPUT_DIR/stats.json (docs/designs/fleet-asset-pipelines.md §4). `-- --glb PATH` measures a local file.
import json, os, shutil, sys, urllib.request
import bpy, bmesh

args = sys.argv[sys.argv.index('--') + 1:] if '--' in sys.argv else []
out = os.environ['FLEET_OUTPUT_DIR']
glb = os.path.join(out, 'model.glb')
if '--glb' in args:
    source = args[args.index('--glb') + 1]
    if os.path.abspath(source) != os.path.abspath(glb):
        shutil.copyfile(source, glb)
else:
    with open(os.path.join(os.environ['FLEET_INPUT_DIR'], 'gen', 'output.json')) as f:
        url = json.load(f)['model_urls']['glb']
    with urllib.request.urlopen(url, timeout=300) as r, open(glb, 'wb') as w:
        shutil.copyfileobj(r, w)

bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=glb)
meshes = [o for o in bpy.context.scene.objects if o.type == 'MESH']
deps = bpy.context.evaluated_depsgraph_get()
triangles, components, uv = 0, 0, bool(meshes)
lo, hi = [float('inf')] * 3, [float('-inf')] * 3
for o in meshes:
    m = o.evaluated_get(deps).to_mesh()
    m.calc_loop_triangles()
    triangles += len(m.loop_triangles)
    uv = uv and len(m.uv_layers) > 0
    bm = bmesh.new()
    bm.from_mesh(m)
    # glTF splits vertices at UV and normal seams; weld by position so a component is a connected piece of surface.
    bmesh.ops.remove_doubles(bm, verts=bm.verts, dist=1e-5)
    bm.verts.index_update()
    seen = set()
    for v in bm.verts:
        if v.index in seen:
            continue
        components += 1
        stack = [v]
        while stack:
            x = stack.pop()
            if x.index in seen:
                continue
            seen.add(x.index)
            stack.extend(e.other_vert(x) for e in x.link_edges)
    bm.free()
    for v in m.vertices:
        w = o.matrix_world @ v.co
        for i in range(3):
            lo[i], hi[i] = min(lo[i], w[i]), max(hi[i], w[i])
    o.evaluated_get(deps).to_mesh_clear()

pbr = {'base_color': False, 'normal': False, 'roughness': False, 'metallic': False}
inputs = {'Base Color': 'base_color', 'Normal': 'normal', 'Roughness': 'roughness', 'Metallic': 'metallic'}
for mat in bpy.data.materials:
    for node in (mat.node_tree.nodes if mat.use_nodes else []):
        if node.type == 'BSDF_PRINCIPLED':
            for name, key in inputs.items():
                pbr[key] = pbr[key] or (name in node.inputs and node.inputs[name].is_linked)
stats = {'triangles': triangles, 'components': components,
         'dimensions_m': [round(hi[i] - lo[i], 4) if meshes else 0 for i in range(3)], 'uv': uv, 'pbr': pbr}
with open(os.path.join(out, 'stats.json'), 'w') as f:
    json.dump(stats, f, indent=1)
print(json.dumps(stats))
