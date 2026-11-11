"""Native S7 checks: guide authority, exact bind and explicit partial coverage; no provider calls."""
import copy, importlib.util, json, os, pathlib, sys, tempfile
import bpy
REPO=pathlib.Path(__file__).resolve().parents[2]
spec=importlib.util.spec_from_file_location('character_build',REPO/'scripts/lab/blender/character-build.py')
build=importlib.util.module_from_spec(spec);spec.loader.exec_module(build)
guide=json.loads((REPO/'config/lab/examples/fleet-uc6-character/rig-guide.json').read_text())
build.validate_guide(guide)
for kind in ['coverage','short-pose','parents','nonfinite','missing-joint']:
    changed=copy.deepcopy(guide)
    if kind=='coverage':changed['coverage']['available']=7
    if kind=='short-pose':changed['poses'][0]['world'].pop()
    if kind=='parents':changed['parents'][1]=1
    if kind=='nonfinite':changed['inverse_bind'][0]=float('nan')
    if kind=='missing-joint':changed['bones'][changed['bones'].index('hand_L')]='invented-other'
    try:build.validate_guide(changed)
    except (ValueError,KeyError):pass
    else:raise AssertionError('invalid guide accepted: '+kind)
with tempfile.TemporaryDirectory(prefix='fleet-character-preflight-') as temporary:
    sys.argv=['character-build.py','--','--validate-guide-only','--guide',str(REPO/'config/lab/examples/fleet-uc6-character/rig-guide.json'),'--out',temporary]
    build.main();result=json.loads((pathlib.Path(temporary)/'output.json').read_text())
    assert result['guide_valid'] and result['coverage']['available']==6 and result['coverage']['missing']==['aim_up']
    assert result['original_seven_pose_acceptance']=='unavailable'
# Optional existing native result: verify actual generated mesh/armature bytes, not a model self-report.
if os.environ.get('FLEET_TEST_CHARACTER_RESULT'):
    result=pathlib.Path(os.environ['FLEET_TEST_CHARACTER_RESULT']);stats=json.loads((result/'character-stats.json').read_text())
    bpy.ops.wm.open_mainfile(filepath=str(result/'character.blend'))
    parts={o.name:o for o in bpy.context.scene.objects if o.type=='MESH' and o.name in guide['part_order']}
    assert set(parts)==set(guide['part_order'])
    arm=[o for o in bpy.context.scene.objects if o.type=='ARMATURE'];assert len(arm)==1 and len(arm[0].data.bones)==94
    for name,obj in parts.items():
        obj.data.calc_loop_triangles();assert len(obj.data.loop_triangles)==stats['parts'][name]['triangles']
        assert len(obj.data.uv_layers)>0
        assert all(abs(sum(group.weight for group in vertex.groups)-1)<1e-6 for vertex in obj.data.vertices)
    assert stats['rig']['rest_max_abs_error_source_units']<1e-4
    assert stats['rig']['cross_island_weights']==0
    assert len(stats['conform']['poses'])==6 and 'aim_up' not in stats['conform']['poses']
    assert stats['conform']['original_seven_pose_acceptance']=='unavailable'
    assert (result/'character.glb').stat().st_size>0
print('S7 native guide checks passed; retained6/7 coverage is not original seven-pose acceptance')
