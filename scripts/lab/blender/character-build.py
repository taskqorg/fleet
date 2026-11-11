"""Fleet's source-projected quad rebuild, CT guide rig and measured capsule conform.
The guide owns dimensions, parts, topology settings, poses and tolerances. Missing poses stay unavailable.
"""
import argparse, hashlib, json, math, os, pathlib, re, shutil, statistics, sys, urllib.request
import bpy, bmesh
from mathutils import Matrix, Vector
from mathutils.bvhtree import BVHTree


def matrix(values):
    if len(values)!=12 or not all(math.isfinite(v) for v in values): raise ValueError('expected finite row-major3x4 transform')
    return Matrix([values[:4],values[4:8],values[8:12],[0,0,0,1]])
def rows(value): return [v for row in value[:3] for v in row]
def axis_point(point,a,b):
    direction=b-a
    return a+direction*max(0.,min(1.,(point-a).dot(direction)/direction.length_squared)) if direction.length_squared else a.copy()
def zone(name):
    for prefix,label in [('head','head'),('neck','neck'),('spine','torso'),('pelvis','pelvis'),('arm_upper','upper_arm'),('arm_lower','forearm'),('hand','hand'),('leg_upper','thigh'),('leg_lower','shin'),('ankle','foot')]:
        if name.startswith(prefix): return label
    return None


def validate_guide(guide):
    names,parents,inverse=guide['bones'],guide['parents'],guide['inverse_bind']
    if len(names)!=94 or len(names)!=len(set(names)) or len(parents)!=len(names) or len(inverse)!=len(names)*12: raise ValueError('incomplete guide skeleton')
    if guide['units']!='Source units' or guide['units_per_metre']<=0: raise ValueError('guide unit convention unavailable')
    required=['pelvis','head_0','neck_0','spine_3']+[part+'_'+side for part in ['arm_upper','arm_lower','hand','leg_upper','leg_lower','ankle'] for side in ['L','R']]
    if not set(required).issubset(names): raise ValueError('CT94 guide lacks required limb joints')
    mapping=guide.get('source_bone_mapping')
    if not isinstance(mapping,dict) or len(set(mapping.values()))!=len(mapping) or not set(required).issubset(mapping.values()) or not all(isinstance(k,str) and k and v in names for k,v in mapping.items()): raise ValueError('source rig mapping is incomplete or ambiguous')
    for i in range(len(names)): matrix(inverse[i*12:i*12+12]).inverted()
    for i,parent in enumerate(parents):
        if not isinstance(parent,int) or parent>=i or parent< -1: raise ValueError('guide parents must precede children')
    for pose in guide['poses']:
        if not isinstance(pose.get('name'),str) or not re.fullmatch(r'[a-z][a-z0-9_]*',pose['name']): raise ValueError('invalid pose artifact name')
        if len(pose['world'])!=len(names)*12: raise ValueError('pose skeleton does not match bind guide')
        for i in range(len(names)): matrix(pose['world'][i*12:i*12+12])
    if len({p['name'] for p in guide['poses']})!=len(guide['poses']): raise ValueError('duplicate guide pose')
    available={p['name'] for p in guide['poses']};missing=set(guide['coverage']['missing'])
    if available&missing or len(available)!=guide['coverage']['available'] or len(available)+len(missing)!=guide['coverage']['expected']:
        raise ValueError('pose coverage contradicts retained data')
    if guide['part_order']!=['head','eyes','mouth','top','gloves','trousers','boots','gear']: raise ValueError('this rebuild requires its declared eight-part profile')


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--glb');parser.add_argument('--guide');parser.add_argument('--out')
    parser.add_argument('--validate-guide-only',action='store_true')
    parser.add_argument('--fetch-source',action='store_true')
    parser.add_argument('--source-rig');parser.add_argument('--bake-glb')
    parser.add_argument('--skip-bake',action='store_true',help='Explicit geometry-only technical qualification; material result remains unqualified')
    args=parser.parse_args(sys.argv[sys.argv.index('--')+1:])
    root=pathlib.Path(os.environ.get('FLEET_INPUT_DIR','.'));out=pathlib.Path(args.out or os.environ['FLEET_OUTPUT_DIR']);out.mkdir(parents=True,exist_ok=True)
    glb=pathlib.Path(args.glb) if args.glb else root/'source'/'source.glb'
    guide_path=pathlib.Path(args.guide) if args.guide else root/'example'/'rig-guide.json'
    guide=json.loads(guide_path.read_text());validate_guide(guide)
    if args.validate_guide_only:
        (out/'output.json').write_text(json.dumps({'guide_valid':True,'guide_sha256':hashlib.sha256(guide_path.read_bytes()).hexdigest(),'coverage':guide['coverage'],'original_seven_pose_acceptance':'unavailable'})+'\n');return
    def download(url,path):
        if not isinstance(url,str) or not url.startswith('https://'):raise ValueError('provider model URL must be HTTPS')
        with urllib.request.urlopen(url,timeout=300) as response,path.open('wb') as target:shutil.copyfileobj(response,target)
    if args.fetch_source:
        generation=json.loads((root/'gen'/'output.json').read_text());urls=generation['model_urls']
        if not generation.get('jobId') or not urls.get('pre_remeshed_glb'):raise ValueError('confirmed generated task and retained high scaffold required')
        download(urls['pre_remeshed_glb'],out/'source.glb');download(urls['glb'],out/'material.glb')
        bpy.ops.wm.read_factory_settings(use_empty=True);bpy.ops.import_scene.gltf(filepath=str(out/'material.glb'))
        meshes=[obj for obj in bpy.context.scene.objects if obj.type=='MESH'];triangles=0
        for obj in meshes:obj.data.calc_loop_triangles();triangles+=len(obj.data.loop_triangles)
        if not meshes or not all(len(obj.data.uv_layers)>0 for obj in meshes):raise ValueError('rig input has no textured UV mesh')
        if not any(node.type=='TEX_IMAGE' and node.image for mat in bpy.data.materials if mat.use_nodes for node in mat.node_tree.nodes):raise ValueError('rig input has no retained image texture')
        if not 0<triangles<=380000:raise ValueError('rig input violates documented380000-triangle provider contract; not submitted')
        result={'input_task_id':generation['jobId'],'height_meters':guide['alignment']['height_source_units']/guide['units_per_metre'],'rig_input_triangles':triangles,'source_sha256':hashlib.sha256((out/'source.glb').read_bytes()).hexdigest(),'material_sha256':hashlib.sha256((out/'material.glb').read_bytes()).hexdigest(),'coverage':guide['coverage']}
        (out/'output.json').write_text(json.dumps(result)+'\n');return
    rig_path=pathlib.Path(args.source_rig) if args.source_rig else out/'source-rig.glb'
    material_path=pathlib.Path(args.bake_glb) if args.bake_glb else root/'source'/'material.glb'
    rig_provenance={'basis':'explicit local paired artifacts; no historical request reconstruction'}
    if not args.source_rig:
        source_record=json.loads((root/'source'/'output.json').read_text());rig_record=json.loads((root/'rig'/'output.json').read_text())
        request=rig_record.get('request',{})
        if not rig_record.get('jobId') or request.get('input_task_id')!=source_record['input_task_id'] or request.get('height_meters')!=source_record['height_meters']:
            raise ValueError('source rig request is not bound to the confirmed generated task/height')
        if hashlib.sha256(glb.read_bytes()).hexdigest()!=source_record['source_sha256'] or hashlib.sha256(material_path.read_bytes()).hexdigest()!=source_record['material_sha256']:
            raise ValueError('retained source model hash mismatch')
        download(rig_record['result']['rigged_character_glb_url'],rig_path)
        rig_provenance={'basis':'retained confirmed pipeline request/receipt','job_id':rig_record['jobId'],'input_task_id':request['input_task_id'],'height_meters':request['height_meters']}
    if not rig_path.exists() or not material_path.exists():raise ValueError('actual source rig and textured material model required; no target-joint fallback')
    names=guide['bones'];indices={name:i for i,name in enumerate(names)};units=guide['units_per_metre'];settings=guide['rebuild']
    bind=[matrix(guide['inverse_bind'][i*12:i*12+12]) for i in range(len(names))];rest=[m.inverted() for m in bind]
    joints={name:rest[i].translation.copy() for i,name in enumerate(names)}
    children={name:[] for name in names}
    for i,parent in enumerate(guide['parents']):
        if parent>=0: children[names[parent]].append(names[i])
    bpy.ops.wm.read_factory_settings(use_empty=True);bpy.ops.import_scene.gltf(filepath=str(glb))
    sources=[obj for obj in bpy.context.scene.objects if obj.type=='MESH']
    if not sources: raise ValueError('source GLB has no mesh')
    for obj in list(bpy.context.scene.objects):
        if obj not in sources: bpy.data.objects.remove(obj,do_unlink=True)
    bpy.ops.object.select_all(action='DESELECT')
    for obj in sources:obj.select_set(True)
    bpy.context.view_layer.objects.active=sources[0];bpy.ops.object.join();source=bpy.context.object;source.name='source-reference'
    bpy.ops.object.transform_apply(location=True,rotation=True,scale=True)
    rotation=Matrix.Rotation(math.radians(guide['alignment']['rotation_z_degrees']),4,'Z')
    for vertex in source.data.vertices: vertex.co=rotation@vertex.co
    lo=Vector([min(v.co[i] for v in source.data.vertices) for i in range(3)]);hi=Vector([max(v.co[i] for v in source.data.vertices) for i in range(3)])
    if hi.z<=lo.z: raise ValueError('source has no vertical extent')
    factor=guide['alignment']['height_source_units']/(hi.z-lo.z)
    center=Vector(((lo.x+hi.x)/2,(lo.y+hi.y)/2,lo.z))
    shift=Vector((joints['pelvis'].x,0,guide['alignment']['floor_source_units']))
    for vertex in source.data.vertices:vertex.co=(vertex.co-center)*factor+shift
    source.data.update();source.data.calc_loop_triangles()
    # Source joints are measured from the paired rig in its own bind frame, then aligned to the retained high scaffold.
    previous=set(bpy.context.scene.objects);bpy.ops.import_scene.gltf(filepath=str(rig_path))
    imported=[obj for obj in bpy.context.scene.objects if obj not in previous];arms=[obj for obj in imported if obj.type=='ARMATURE'];rig_meshes=[obj for obj in imported if obj.type=='MESH']
    if len(arms)!=1 or not rig_meshes:raise ValueError('source rig must contain one armature and its reference mesh')
    rig_points=[rotation@(obj.matrix_world@vertex.co) for obj in rig_meshes for vertex in obj.data.vertices]
    rig_lo=Vector([min(point[i] for point in rig_points) for i in range(3)]);rig_hi=Vector([max(point[i] for point in rig_points) for i in range(3)])
    if rig_hi.z<=rig_lo.z:raise ValueError('source rig bounds unavailable')
    rig_factor=guide['alignment']['height_source_units']/(rig_hi.z-rig_lo.z);rig_center=Vector(((rig_lo.x+rig_hi.x)/2,(rig_lo.y+rig_hi.y)/2,rig_lo.z))
    provider_bones={bone.name.split(':')[-1]:bone for bone in arms[0].data.bones};source_joints={};mapping=guide['source_bone_mapping']
    for provider,target in mapping.items():
        if provider not in provider_bones or target not in indices:raise ValueError('source rig joint mapping unavailable: '+provider)
        source_joints[target]=(rotation@(arms[0].matrix_world@provider_bones[provider].head_local)-rig_center)*rig_factor+shift
    target_joints={name:point.copy() for name,point in joints.items()}
    endpoint={'pelvis':'spine_1','spine_1':'spine_2','spine_2':'spine_3','spine_3':'neck_0','neck_0':'head_0'}
    for side in ['L','R']:
        endpoint.update({'clavicle_'+side:'arm_upper_'+side,'arm_upper_'+side:'arm_lower_'+side,'arm_lower_'+side:'hand_'+side,'leg_upper_'+side:'leg_lower_'+side,'leg_lower_'+side:'ankle_'+side,'ankle_'+side:'ball_'+side})
    source_to_target={}
    for name in source_joints:
        if name in endpoint:
            end=endpoint[name];a=source_joints[end]-source_joints[name];b=target_joints[end]-target_joints[name]
            if min(a.length,b.length)<1e-6:raise ValueError('degenerate source rig segment: '+name)
            orient=a.rotation_difference(b).to_matrix().to_4x4();scale=b.length/a.length
        else:
            parent='neck_0' if name=='head_0' else 'arm_lower_'+name[-1] if name.startswith('hand_') else 'ankle_'+name[-1]
            a=source_joints[name]-source_joints[parent];b=target_joints[name]-target_joints[parent]
            orient=a.rotation_difference(b).to_matrix().to_4x4() if min(a.length,b.length)>1e-6 else Matrix.Identity(4);scale=1.
        source_to_target[name]=Matrix.Translation(target_joints[name])@orient@Matrix.Scale(scale,4)@Matrix.Translation(-source_joints[name])
    # Unmapped helper/finger joints keep their guide-relative positions under the nearest mapped ancestor, never another limb.
    for i,name in enumerate(names):
        if name in source_joints:continue
        parent=guide['parents'][i]
        while parent>=0 and names[parent] not in source_to_target:parent=guide['parents'][parent]
        if parent<0:source_to_target[name]=Matrix.Identity(4);source_joints[name]=target_joints[name].copy()
        else:
            source_to_target[name]=source_to_target[names[parent]].copy();source_joints[name]=source_to_target[name].inverted()@target_joints[name]
    joints=source_joints
    rig_provenance.update(sha256=hashlib.sha256(rig_path.read_bytes()).hexdigest(),material_sha256=hashlib.sha256(material_path.read_bytes()).hexdigest(),provider_joint_mapping=mapping,source_joints={name:list(value) for name,value in source_joints.items()},target_joints={name:list(value) for name,value in target_joints.items()},source_to_target={name:rows(value) for name,value in source_to_target.items()})
    for obj in imported:bpy.data.objects.remove(obj,do_unlink=True)
    source_positions=[v.co.copy() for v in source.data.vertices]
    source_faces=[tuple(t.vertices) for t in source.data.loop_triangles]
    full_tree=BVHTree.FromPolygons(source_positions,source_faces,all_triangles=True)
    floor=guide['alignment']['floor_source_units'];height=guide['alignment']['height_source_units'];boot_top=min(joints['ankle_L'].z,joints['ankle_R'].z)+6
    neck_z=joints['neck_0'].z;waist=joints['pelvis'].z+2
    def region(point):
        if point.z>neck_z: return 'head'
        if point.z<boot_top:return 'boots'
        for side in ['L','R']:
            if (point-joints['hand_'+side]).length<7 and abs(point.y)>abs(joints['hand_'+side].y)-3: return 'gloves'
        if point.z<waist:return 'trousers'
        return 'top'
    faces_by_part={name:[] for name in guide['part_order']}
    for face in source_faces:
        point=sum((source_positions[i] for i in face),Vector())/3
        faces_by_part[region(point)].append(face)
    trees={name:BVHTree.FromPolygons(source_positions,faces,all_triangles=True) for name,faces in faces_by_part.items() if faces}
    capsule_bind=[]
    for box in guide['hitboxes']:
        i=box['bone'];capsule_bind.append({'bone':names[i],'zone':zone(names[i]),'a':rest[i]@Vector(box['a']),'b':rest[i]@Vector(box['b']),'radius':box['radius']})
    vertices={p:[] for p in guide['part_order']};faces={p:[] for p in guide['part_order']};meta={p:[] for p in guide['part_order']}
    fallback={p:0 for p in guide['part_order']}
    def add(part,point,allowed,island):
        vertices[part].append(point.copy());meta[part].append({'allowed':allowed,'island':island});return len(vertices[part])-1
    def radius_for(point,allowed):
        boxes=[box for box in capsule_bind if box['bone'] in allowed]
        if not boxes:return max(height*.008,.1)
        return min(boxes,key=lambda box:(point-axis_point(point,box['a'],box['b'])).length-box['radius'])['radius']
    def tube(part,centers,sides,allowed,island):
        tree=trees.get(part);rings=[]
        for i,centerline in enumerate(centers):
            tangent=(centers[min(i+1,len(centers)-1)]-centers[max(0,i-1)]).normalized()
            u=tangent.cross(Vector((0,1,0)))
            if u.length<.01:u=tangent.cross(Vector((1,0,0)))
            u.normalize();v=tangent.cross(u).normalized();radii=[];directions=[]
            for j in range(sides):
                direction=u*math.cos(2*math.pi*j/sides)+v*math.sin(2*math.pi*j/sides);directions.append(direction)
                hit=tree.ray_cast(centerline,direction,height) if tree else (None,None,None,None)
                if hit[0] is None:
                    radii.append(radius_for(centerline,allowed));fallback[part]+=1
                else:radii.append((hit[0]-centerline).length)
                if part=='gloves' and not island.startswith('palm'):
                    radii[-1]=min(radii[-1],radius_for(centerline,allowed)*.15)
                if part=='head' and (i==0 or i==len(centers)-1) and hit[0] is None:
                    radii[-1]=min(radii[-1],height*.001)
            limit=statistics.median(radii)*settings['radius_clamp_median']
            rings.append([add(part,centerline+directions[j]*min(radii[j],limit),allowed,island) for j in range(sides)])
        for first,second in zip(rings,rings[1:]):
            for j in range(sides):faces[part].append((first[j],first[(j+1)%sides],second[(j+1)%sides],second[j]))
        faces[part].append(tuple(reversed(rings[0])));faces[part].append(tuple(rings[-1]))
    def chain(points,steps=5):
        result=[];offset=settings['joint_offset_source_units']
        for first,second in zip(points,points[1:]):
            length=(second-first).length
            samples=sorted(set([j/steps for j in range(steps)]+[min(.49,offset/max(length,.001)),max(.51,1-offset/max(length,.001))]))
            result.extend(first.lerp(second,t) for t in samples)
        return result+[points[-1]]
    head_radius=next(box['radius'] for box in guide['hitboxes'] if names[box['bone']]=='head_0')
    head_top=max(point.z for point in source_positions)-height*1e-5
    head_bottom=neck_z-.5
    head_centers=[]
    for i in range(32):
        z=head_bottom+(head_top-head_bottom)*i/31
        section=[point for point in source_positions if abs(point.z-z)<(head_top-head_bottom)/32]
        head_centers.append(Vector((statistics.median(p.x for p in section),statistics.median(p.y for p in section),z)) if section else Vector((joints['head_0'].x,0,z)))
    tube('head',head_centers,settings['ring_sides']['head'],['head_0','neck_0'],'head')
    torso_bones=[name for name in ['pelvis','spine_0','spine_1','spine_2','spine_3','neck_0'] if name in indices]
    torso_centers=[Vector((joints['pelvis'].x,0,waist))]+[joints[name] for name in torso_bones if waist<joints[name].z<neck_z]+[joints['neck_0']]
    tube('top',chain(torso_centers,3),settings['ring_sides']['torso'],torso_bones,'torso')
    pelvis_low=min(joints['leg_upper_L'].z,joints['leg_upper_R'].z)-1
    tube('trousers',[Vector((joints['pelvis'].x,0,pelvis_low+(waist+1-pelvis_low)*i/7)) for i in range(8)],settings['ring_sides']['pelvis'],['pelvis'],'pelvis')
    for side in ['L','R']:
        arm=['arm_upper_'+side,'arm_lower_'+side,'hand_'+side]
        tube('top',chain([joints[name] for name in arm]),settings['ring_sides']['limb'],arm,'sleeve-'+side)
        leg=['leg_upper_'+side,'leg_lower_'+side,'ankle_'+side]
        end=joints[leg[-1]].copy();end.z=boot_top-1
        tube('trousers',chain([joints[leg[0]],joints[leg[1]],end]),settings['ring_sides']['limb'],leg[:2],'leg-'+side)
        ankle=joints['ankle_'+side]
        tube('boots',[Vector((ankle.x+(1-t)*2,ankle.y,floor+(boot_top-floor)*t)) for t in [i/7 for i in range(8)]],settings['ring_sides']['boots'],['ankle_'+side,'leg_lower_'+side],'boot-'+side)
        fingers=[name for name in names if name.startswith('finger_') and name.endswith('_'+side)]
        middle=[joints[name] for name in fingers if name.startswith('finger_middle_')]
        palm_end=sum(middle,Vector())/len(middle) if middle else joints['hand_'+side]+Vector((0,0,-3))
        glove_allowed=['hand_'+side]+fingers
        tube('gloves',chain([joints['hand_'+side],palm_end],3),settings['ring_sides']['gloves'],glove_allowed,'palm-'+side)
        for finger in ['thumb','index','middle','ring','pinky']:
            bones=[name for name in fingers if name.startswith('finger_'+finger+'_') and '_meta_' not in name]
            bones.sort()
            if len(bones)>1:tube('gloves',chain([joints[name] for name in bones],2),settings['ring_sides']['gloves'],['hand_'+side]+bones,finger+'-'+side)
    def patch(part,center,width,length,offset,island,allowed):
        grid=[]
        for row in range(5):
            line=[]
            for col in range(9):
                point=Vector((center.x,center.y+(col/8-.5)*width,center.z+(row/4-.5)*length))
                hit=full_tree.ray_cast(point+Vector((height,0,0)),Vector((-1,0,0)),height*2)
                if hit[0] is None:
                    hit=full_tree.find_nearest(point)
                    if hit[0] is None:raise ValueError('source surface unavailable for '+part+' patch')
                    fallback[part]+=1
                line.append(add(part,hit[0]+hit[1]*offset,allowed,island))
            grid.append(line)
        for first,second in zip(grid,grid[1:]):
            for col in range(8):faces[part].append((first[col],first[col+1],second[col+1],second[col]))
    for side in ['L','R']:
        eye=joints.get('eyeball_'+side.lower(),joints['head_0']+Vector((head_radius*.7,(1 if side=='L' else -1)*head_radius*.3,head_radius*.25)))
        # UV spheres have owned topology; source colour is baked onto the rebuilt surface.
        bm=bmesh.new();bmesh.ops.create_uvsphere(bm,u_segments=16,v_segments=8,radius=head_radius*.09)
        offset=len(vertices['eyes']);bm.verts.ensure_lookup_table()
        for vertex in bm.verts:add('eyes',eye+vertex.co,['head_0'],'eye-'+side)
        bm.verts.index_update()
        for face in bm.faces:faces['eyes'].append(tuple(offset+v.index for v in face.verts))
        bm.free()
    mouth=joints['head_0'].copy();mouth.z-=head_radius*.45
    patch('mouth',mouth,head_radius*.7,head_radius*.16,settings['shell_offset_metres']['mouth']*units,'mouth',['head_0'])
    for sign in [-1,1]:
        center=joints['spine_3'].copy();center.y=sign*head_radius
        patch('gear',center,head_radius*.45,head_radius*2,settings['shell_offset_metres']['gear']*units,'strap-'+str(sign),torso_bones)
    patch('gear',joints['pelvis']+Vector((0,0,2)),head_radius*2.5,head_radius*.35,settings['shell_offset_metres']['gear']*units,'belt',['pelvis'])
    objects={};weights={}
    for part in guide['part_order']:
        if not vertices[part] or not faces[part]: raise ValueError('part construction empty: '+part)
        mesh=bpy.data.meshes.new(part);mesh.from_pydata(vertices[part],[],faces[part]);mesh.update();obj=bpy.data.objects.new(part,mesh);bpy.context.collection.objects.link(obj);objects[part]=obj
        bm=bmesh.new();bm.from_mesh(mesh);bmesh.ops.recalc_face_normals(bm,faces=list(bm.faces));bm.to_mesh(mesh);bm.free()
        assigned=[]
        for vertex,info in zip(mesh.vertices,meta[part]):
            distances=[]
            for bone in info['allowed']:
                i=indices[bone];ends=children[bone]
                end=joints[ends[0]] if ends else joints[bone]+rest[i].to_3x3()@Vector((0,1,0))
                distance=(vertex.co-axis_point(vertex.co,joints[bone],end)).length
                distances.append((distance,bone))
            nearest=sorted(distances)[:2];values=[1/max(distance,.05)**2 for distance,bone in nearest];total=sum(values)
            w={bone:value/total for (distance,bone),value in zip(nearest,values)};assigned.append(w)
        weights[part]=assigned
    # UVs and maps are authored on the rebuilt meshes, never carried over from the provider mesh.
    previous=set(bpy.context.scene.objects);bpy.ops.import_scene.gltf(filepath=str(material_path))
    material_objects=[obj for obj in bpy.context.scene.objects if obj not in previous];material_meshes=[obj for obj in material_objects if obj.type=='MESH'];material_other=[obj for obj in material_objects if obj.type!='MESH']
    if not material_meshes:raise ValueError('textured source reference has no mesh')
    bpy.ops.object.select_all(action='DESELECT')
    for obj in material_meshes:obj.select_set(True)
    bpy.context.view_layer.objects.active=material_meshes[0];bpy.ops.object.join();texture_source=bpy.context.object;texture_source.name='material-source-reference';bpy.ops.object.transform_apply(location=True,rotation=True,scale=True)
    for vertex in texture_source.data.vertices:vertex.co=(rotation@vertex.co-center)*factor+shift
    texture_source.data.update()
    for obj in material_other:bpy.data.objects.remove(obj,do_unlink=True)
    materials={};bake_status='skipped-explicitly' if args.skip_bake else 'pending'
    (out/'textures').mkdir(exist_ok=True);scene=bpy.context.scene
    scene.render.engine='CYCLES';scene.cycles.samples=1;scene.render.bake.use_selected_to_active=True;scene.render.bake.cage_extrusion=.5;scene.render.bake.max_ray_distance=5
    for part,obj in objects.items():
        bpy.ops.object.select_all(action='DESELECT');obj.select_set(True);bpy.context.view_layer.objects.active=obj;bpy.ops.object.mode_set(mode='EDIT');bpy.ops.mesh.select_all(action='SELECT');bpy.ops.uv.smart_project(island_margin=.02);bpy.ops.object.mode_set(mode='OBJECT')
        mat=bpy.data.materials.new(part+'-own');mat.use_nodes=True;obj.data.materials.append(mat);nodes=mat.node_tree.nodes;shader=nodes.get('Principled BSDF');images={}
        for kind in ['base_color','normal']:
            image=bpy.data.images.new(part+'-'+kind,width=settings['atlas_size'],height=settings['atlas_size'],alpha=False)
            if kind=='normal':image.colorspace_settings.name='Non-Color'
            node=nodes.new('ShaderNodeTexImage');node.image=image;nodes.active=node
            if not args.skip_bake:
                bpy.ops.object.select_all(action='DESELECT');texture_source.select_set(True);obj.select_set(True);bpy.context.view_layer.objects.active=obj
                bpy.ops.object.bake(type='DIFFUSE' if kind=='base_color' else 'NORMAL',pass_filter={'COLOR'} if kind=='base_color' else set())
                image.filepath_raw=str((out/'textures'/(part+'-'+kind+'.png')).resolve());image.file_format='PNG';image.save()
            images[kind]=(image,node)
        mat.node_tree.links.new(images['base_color'][1].outputs['Color'],shader.inputs['Base Color'])
        normal=nodes.new('ShaderNodeNormalMap');mat.node_tree.links.new(images['normal'][1].outputs['Color'],normal.inputs['Color']);mat.node_tree.links.new(normal.outputs['Normal'],shader.inputs['Normal'])
        for image,node in images.values():image.pack()
        materials[part]={'uv_layers':len(obj.data.uv_layers),'baked':not args.skip_bake}
    if not args.skip_bake:bake_status='completed'

    # Retarget the own rebuilt surface after its source-space bake; the provider mesh remains a reference.
    for part,obj in objects.items():
        for vertex,w in zip(obj.data.vertices,weights[part]):vertex.co=sum((source_to_target[bone]@vertex.co*weight for bone,weight in w.items()),Vector())
        obj.data.update()
    # Keep per-island constraints through every posed correction and measurement.
    pose_capsules={};skin={}
    for pose in guide['poses']:
        world=[matrix(pose['world'][i*12:i*12+12]) for i in range(len(names))]
        pose_capsules[pose['name']]=[{'bone':names[box['bone']],'zone':zone(names[box['bone']]),'a':world[box['bone']]@Vector(box['a']),'b':world[box['bone']]@Vector(box['b']),'radius':box['radius']} for box in guide['hitboxes']]
        skin[pose['name']]=[world[i]@bind[i] for i in range(len(names))]
    def weighted(pose,w):
        value=Matrix([[0.]*4 for _ in range(4)])
        for bone,weight in w.items():value+=skin[pose][indices[bone]]*weight
        return value
    transforms={part:{pose:[weighted(pose,w) for w in assigned] for pose in skin} for part,assigned in weights.items()}
    attachments={}
    for part in ['eyes','mouth','gear']:
        attached=[]
        for vertex,info in zip(objects[part].data.vertices,meta[part]):
            body='head' if part in ['eyes','mouth'] else 'trousers' if info['island']=='belt' else 'top'
            mesh=objects[body].data;mesh.calc_loop_triangles();tree=BVHTree.FromPolygons([v.co for v in mesh.vertices],[tuple(t.vertices) for t in mesh.loop_triangles],all_triangles=True)
            nearest=tree.find_nearest(vertex.co)
            if nearest[0] is None:raise ValueError('secondary surface attachment unavailable')
            triangle=tuple(mesh.loop_triangles[nearest[2]].vertices);a,b,c=[mesh.vertices[i].co.copy() for i in triangle]
            u,v=b-a,c-a;q=nearest[0]-a;uu,uv,vv=u.dot(u),u.dot(v),v.dot(v);denom=uu*vv-uv*uv
            if abs(denom)<1e-12:bary=(1.,0.,0.)
            else:
                y=(vv*q.dot(u)-uv*q.dot(v))/denom;z=(uu*q.dot(v)-uv*q.dot(u))/denom;bary=(1-y-z,y,z)
            attached.append((body,triangle,bary,vertex.co-nearest[0]))
        attachments[part]=attached
    rounds=[]
    for iteration in range(settings['conform_rounds']):
        movement=[]
        for part,obj in objects.items():
            if part in ['eyes','mouth','gear']:continue
            correction=[Vector() for _ in obj.data.vertices];counts=[0]*len(correction)
            for pose in skin:
                for i,vertex in enumerate(obj.data.vertices):
                    allowed=meta[part][i]['allowed'];boxes=[box for box in pose_capsules[pose] if box['bone'] in allowed and box['zone']]
                    if not boxes:continue
                    transform=transforms[part][pose][i];point=transform@vertex.co
                    box=min(boxes,key=lambda b:(point-axis_point(point,b['a'],b['b'])).length-b['radius'])
                    foot=axis_point(point,box['a'],box['b']);direction=point-foot;distance=direction.length
                    if distance<1e-8:continue
                    tolerance=guide['tolerance']['zones'][box['zone']]['outsideMax']
                    target=min(distance,box['radius']+tolerance)
                    if abs(distance-target)<1e-5:continue
                    desired=foot+direction*(target/distance)
                    correction[i]+=transform.inverted_safe()@desired-vertex.co;counts[i]+=1
            correction=[value/count if count else value for value,count in zip(correction,counts)]
            edges=[(edge.vertices[0],edge.vertices[1]) for edge in obj.data.edges]
            for _ in range(settings['smooth_rounds']):
                sums=[value.copy() for value in correction];neighbors=[1]*len(correction)
                for a,b in edges:
                    if meta[part][a]['island']!=meta[part][b]['island']:continue
                    sums[a]+=correction[b];sums[b]+=correction[a];neighbors[a]+=1;neighbors[b]+=1
                correction=[value/count for value,count in zip(sums,neighbors)]
            for vertex,delta in zip(obj.data.vertices,correction):vertex.co+=delta;movement.append(delta.length)
            obj.data.update()
        rounds.append({'round':iteration,'mean_move_source_units':statistics.mean(movement) if movement else None,'max_move_source_units':max(movement) if movement else None})
    for part,attached in attachments.items():
        for vertex,(body,triangle,bary,offset) in zip(objects[part].data.vertices,attached):
            vertex.co=sum((objects[body].data.vertices[index].co*weight for index,weight in zip(triangle,bary)),Vector())+offset
        objects[part].data.update()
    # Construct the exact supplied skeleton basis; no guessed or synthetic missing pose is added.
    arm_data=bpy.data.armatures.new('character-guide');arm=bpy.data.objects.new('character-guide',arm_data);bpy.context.collection.objects.link(arm)
    bpy.ops.object.select_all(action='DESELECT');arm.select_set(True);bpy.context.view_layer.objects.active=arm;bpy.ops.object.mode_set(mode='EDIT')
    for i,name in enumerate(names):
        bone=arm_data.edit_bones.new(name);bone.head=(0,0,0);bone.tail=(0,1,0);bone.matrix=rest[i]
        if guide['parents'][i]>=0:bone.parent=arm_data.edit_bones[names[guide['parents'][i]]]
    bpy.ops.object.mode_set(mode='OBJECT')
    bind_error=max(abs(arm_data.bones[name].matrix_local[row][col]-rest[i][row][col]) for i,name in enumerate(names) for row in range(4) for col in range(4))
    for part,obj in objects.items():
        for name in set(b for w in weights[part] for b in w):obj.vertex_groups.new(name=name)
        for i,w in enumerate(weights[part]):
            for bone,weight in w.items():obj.vertex_groups[bone].add([i],weight,'REPLACE')
        modifier=obj.modifiers.new('guide-rig','ARMATURE');modifier.object=arm
    fit={}
    for pose in skin:
        posed=[];triangles=[];allowed_points=[]
        for part,obj in objects.items():
            offset=len(posed);obj.data.calc_loop_triangles()
            for i,vertex in enumerate(obj.data.vertices):posed.append(transforms[part][pose][i]@vertex.co);allowed_points.append(meta[part][i]['allowed'])
            triangles.extend(tuple(offset+i for i in triangle.vertices) for triangle in obj.data.loop_triangles)
        surface=BVHTree.FromPolygons(posed,triangles,all_triangles=True);metrics={}
        for box in pose_capsules[pose]:
            if not box['zone']:continue
            samples=[];outside=[]
            axis=(box['b']-box['a']).normalized();u=axis.cross(Vector((0,0,1)))
            if u.length<.01:u=axis.cross(Vector((0,1,0)))
            u.normalize();v=axis.cross(u).normalized()
            for t in [0,.25,.5,.75,1]:
                for j in range(16):
                    point=box['a'].lerp(box['b'],t)+(u*math.cos(j*math.pi/8)+v*math.sin(j*math.pi/8))*box['radius']
                    nearest=surface.find_nearest(point);samples.append(nearest[3] if nearest[0] is not None else None)
            group=metrics.setdefault(box['zone'],{'outside':[],'gap':[]});group['gap'].extend(samples)
        for point,allowed in zip(posed,allowed_points):
            boxes=[box for box in pose_capsules[pose] if box['bone'] in allowed and box['zone']]
            if not boxes:continue
            box=min(boxes,key=lambda box:(point-axis_point(point,box['a'],box['b'])).length-box['radius'])
            distance=max(0.,(point-axis_point(point,box['a'],box['b'])).length-box['radius'])
            metrics.setdefault(box['zone'],{'outside':[],'gap':[]})['outside'].append(distance)
        result={};passed=True
        for label,values in metrics.items():
            limit=guide['tolerance']['zones'][label];observed={}
            for key,source_values in [('outside',values['outside']),('gap',values['gap'])]:
                observed[key+'Median']=statistics.median(source_values) if source_values and all(v is not None for v in source_values) else None;observed[key+'Max']=max(source_values) if source_values and all(v is not None for v in source_values) else None
            # These sampled capsule/surface distances are not the original winding-number fit oracle.
            ok=all(value is not None and math.isfinite(value) and value<=limit[key] for key,value in observed.items());passed=passed and ok
            result[label]={'measured':observed,'source_tolerance':limit,'within_source_distance_limits':ok}
        fit[pose]={'zones':result,'within_source_distance_limits':passed,'oracle':'Fleet sampled capsule/surface distances; not original seven-pose fit equivalence'}
    totals={};wrong=0;weight_error=0
    for part,obj in objects.items():
        obj.data.calc_loop_triangles();bm=bmesh.new();bm.from_mesh(obj.data)
        totals[part]={'vertices':len(obj.data.vertices),'triangles':len(obj.data.loop_triangles),'faces':len(obj.data.polygons),'quads':sum(len(face.vertices)==4 for face in obj.data.polygons),'boundary_edges':sum(edge.is_boundary for edge in bm.edges),'nonmanifold_edges':sum(not edge.is_manifold for edge in bm.edges),'source_projection_fallback_vertices':fallback[part],**materials[part]};bm.free()
        for info,w in zip(meta[part],weights[part]):wrong+=sum(bone not in info['allowed'] for bone in w);weight_error=max(weight_error,abs(sum(w.values())-1))
    # The immutable source is retained in its own artifact, not duplicated inside every result blend.
    bpy.data.objects.remove(source,do_unlink=True);bpy.data.objects.remove(texture_source,do_unlink=True)
    # Explicit world scale makes exported glTF metres independent of Blender scene-unit display settings.
    for obj in [arm,*objects.values()]:obj.scale=(1/units,)*3
    bpy.ops.object.select_all(action='DESELECT')
    for obj in [arm,*objects.values()]:obj.select_set(True)
    bpy.context.view_layer.objects.active=arm;bpy.ops.wm.save_as_mainfile(filepath=str((out/'character.blend').resolve()))
    bpy.ops.export_scene.gltf(filepath=str((out/'character.glb').resolve()),export_format='GLB',use_selection=True,export_yup=True,export_skins=True)
    report={'source_sha256':hashlib.sha256(glb.read_bytes()).hexdigest(),'guide_sha256':hashlib.sha256(guide_path.read_bytes()).hexdigest(),'source_rig':rig_provenance,'part_order':guide['part_order'],'parts':totals,'triangles':sum(p['triangles'] for p in totals.values()),'rig':{'bones':len(names),'rest_max_abs_error_source_units':bind_error,'cross_island_weights':wrong,'weight_sum_max_abs_error':weight_error},'conform':{'coverage':guide['coverage'],'rounds':rounds,'poses':fit,'original_seven_pose_acceptance':'unavailable','remaining_defects':'Inspect retained topology, distance metrics and renders; sampled distances are not the original source winding-number oracle.'},'materials':bake_status,'alignment':{**guide['alignment'],'scale_to_source_units':factor},'status':'technical-result; owner aesthetic and game acceptance remain separate'}
    (out/'character-stats.json').write_text(json.dumps(report,indent=2,allow_nan=False)+'\n');(out/'output.json').write_text(json.dumps({'model':'character.glb','stats':'character-stats.json','coverage':guide['coverage'],'materials':bake_status})+'\n')

if __name__=='__main__':main()
