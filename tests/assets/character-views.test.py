"""Requirement: S7 view identity before paid submit; risk: guessed ordering or corrupted bytes reach generation."""
import base64, copy, hashlib, importlib.util, json, os, pathlib, struct, subprocess, tempfile, unittest, zlib

REPO=pathlib.Path(__file__).resolve().parents[2]
spec=importlib.util.spec_from_file_location('character_views',REPO/'scripts/lab/character-views.py')
views=importlib.util.module_from_spec(spec);spec.loader.exec_module(views)
def png(width=3,height=2):
    def chunk(kind,body): return struct.pack('>I',len(body))+kind+body+struct.pack('>I',zlib.crc32(kind+body)&0xffffffff)
    return b'\x89PNG\r\n\x1a\n'+chunk(b'IHDR',struct.pack('>IIBBBBB',width,height,8,2,0,0,0))+chunk(b'sRGB',b'\0')+chunk(b'IDAT',zlib.compress((b'\0'+b'\xff\x80\x40'*width)*height))+chunk(b'IEND',b'')
def fixture(root):
    raw=png();hash=hashlib.sha256(raw).hexdigest();(root/'source.png').write_bytes(raw)
    provenance={'state':'ready-for-mapping','sources':[{'index':0,'path':'source.png','sha256':hash,'bytes':len(raw)}],'candidates':[]}
    mapping={'views':[]}
    for index,name in enumerate(['front','left','back']):
        file=str(index)+'.png';(root/file).write_bytes(raw)
        provenance['candidates'].append({'index':index,'source_index':0,'source_sha256':hash,'path':file,'sha256':hash,'width':3,'height':2,'media_type':'image/png'})
        mapping['views'].append({'candidate_index':index,'source_sha256':hash,'sha256':hash,'view':name,'basis':'agent-inspected','rationale':'Invented labelled fixture, not a real visual judgement.'})
    return provenance,mapping
class Encoder(unittest.TestCase):
    def test_inspected_order_and_rejections(self):
        with tempfile.TemporaryDirectory() as d:
            root=pathlib.Path(d);provenance,mapping=fixture(root)
            mapping['views'].reverse()
            encoded=views.encode_views(root,provenance,mapping,['front','left','back'])
            self.assertEqual(len(encoded),3);self.assertEqual(base64.b64decode(encoded[0].split(',')[1]),png())
            for key,value in [('view','unknown'),('candidate_index',999),('source_sha256','0'*64),('sha256','0'*64),('basis','provider-order'),('rationale','')]:
                changed=copy.deepcopy(mapping);changed['views'][0][key]=value
                with self.subTest(key=key),self.assertRaises(ValueError):views.encode_views(root,provenance,changed,['front','left','back'])
            for count in [2,4]:
                changed=copy.deepcopy(mapping);changed['views']=changed['views'][:count] if count==2 else changed['views']+[changed['views'][0]]
                with self.assertRaises(ValueError):views.encode_views(root,provenance,changed,['front','left','back'])
            changed=copy.deepcopy(provenance);changed['candidates'][0]['width']=4
            with self.assertRaises(ValueError):views.encode_views(root,changed,mapping,['front','left','back'])
            (root/'0.png').write_bytes(png()+b'corrupt')
            with self.assertRaises(ValueError):views.encode_views(root,provenance,mapping,['front','left','back'])
    def test_actual_png_bytes(self):
        self.assertEqual(views.png_dimensions(png()),(3,2))
        for data in [b'not PNG',png()[:-1],png()+b'junk',png().replace(b'IDAT',b'IDAX')]:
            with self.assertRaises(ValueError):views.png_dimensions(data)
    def test_payload_contract_precedes_submit(self):
        with tempfile.TemporaryDirectory() as d:
            root=pathlib.Path(d);compose=root/'compose';compose.mkdir();mappingdir=root/'map';mappingdir.mkdir();out=root/'out';out.mkdir()
            provenance,mapping=fixture(compose);provenance.update(size=3,padding=0,grid='3,1')
            (compose/'provenance.json').write_text(json.dumps(provenance));(mappingdir/'view-map.json').write_text(json.dumps(mapping))
            command=['python3',str(REPO/'scripts/lab/character-views.py'),'--request-kind','multi','--max-request-bytes','1']
            env={**os.environ,'FLEET_INPUT_DIR':str(root),'FLEET_OUTPUT_DIR':str(out)}
            bad=subprocess.run(command,env=env,capture_output=True)
            self.assertNotEqual(bad.returncode,0);self.assertIn(b'body contract',bad.stderr);self.assertFalse((out/'output.json').exists())
            command[-1]='1048576';subprocess.run(command,env=env,check=True,capture_output=True)
            result=json.loads((out/'output.json').read_text());self.assertEqual(len(result['request']['image_urls']),3)
            self.assertTrue(result['request']['save_pre_remeshed_model']);self.assertEqual(result['request']['target_polycount'],200000);self.assertEqual(result['request']['pose_mode'],'a-pose')

@unittest.skipUnless(os.environ.get('FLEET_TEST_BLENDER'),'Blender native qualification runs separately on owner-mac')
class Composer(unittest.TestCase):
    def run_composer(self,root,mode):
        out=root/'out';out.mkdir(exist_ok=True)
        env={**os.environ,'FLEET_INPUT_DIR':str(root),'FLEET_OUTPUT_DIR':str(out),'FLEET_BLENDER':os.environ['FLEET_TEST_BLENDER']}
        return subprocess.run(['sh',str(REPO/'scripts/lab/blender/run.sh'),'compose-views','--mode',mode,'--size','32'],env=env,capture_output=True)
    def test_sheet_and_retained_extra_sources(self):
        with tempfile.TemporaryDirectory() as d:
            root=pathlib.Path(d);(root/'example').mkdir();(root/'example'/'turnaround.png').write_bytes(png(96,32))
            result=self.run_composer(root,'fixture-sheet');self.assertEqual(result.returncode,0,result.stderr.decode())
            p=json.loads((root/'out'/'provenance.json').read_text());self.assertEqual(p['state'],'ready-for-mapping');self.assertEqual(len(p['candidates']),3)
            for candidate in p['candidates']:self.assertEqual(views.png_dimensions((root/'out'/candidate['path']).read_bytes()),(32,32))
        with tempfile.TemporaryDirectory() as d:
            root=pathlib.Path(d);(root/'prep').mkdir()
            part={'inlineData':{'mimeType':'image/png','data':base64.b64encode(png()).decode()}}
            (root/'prep'/'output.json').write_text(json.dumps({'candidates':[{'content':{'parts':[part,part]}}]}))
            result=self.run_composer(root,'gemini-sheet')
            self.assertNotEqual(result.returncode,0,'Blender Python exception must fail the pipeline step')
            p=json.loads((root/'out'/'provenance.json').read_text());self.assertEqual(p['state'],'failed');self.assertEqual(len(p['sources']),2)
            self.assertTrue(all((root/'out'/s['path']).exists() for s in p['sources']));self.assertFalse((root/'out'/'output.json').exists())
    def test_canonical_reference_and_hf_transport(self):
        # Requirement: actual fixture mapping and all explicit HF outputs survive staging; risk: source hash drift or dropped angle bytes.
        with tempfile.TemporaryDirectory() as d:
            root=pathlib.Path(d);(root/'example').mkdir()
            for name in ['turnaround.png','rig-guide.json']:
                (root/'example'/name).write_bytes((REPO/'config/lab/examples/fleet-uc6-character'/name).read_bytes())
            out=root/'references';out.mkdir()
            env={**os.environ,'FLEET_INPUT_DIR':str(root),'FLEET_OUTPUT_DIR':str(out),'FLEET_BLENDER':os.environ['FLEET_TEST_BLENDER']}
            command=['sh',str(REPO/'scripts/lab/blender/run.sh'),'compose-views','--mode','fixture-sheet','--reference-map','--size','32']
            r=subprocess.run(command,env=env,capture_output=True);self.assertEqual(r.returncode,0,r.stderr.decode())
            provenance=json.loads((out/'provenance.json').read_text());mapping=json.loads((out/'view-map.json').read_text())
            self.assertEqual(len(views.encode_views(out,provenance,mapping,['front','left','back'])),3)
            for angle in ['right','back','left']:
                folder=root/('hf-'+angle);folder.mkdir();(folder/'output.json').write_text(json.dumps({'images':[{'url':'data:image/png;base64,'+base64.b64encode(png()).decode()}]}))
            compose=root/'compose';compose.mkdir();env['FLEET_OUTPUT_DIR']=str(compose)
            r=subprocess.run(['sh',str(REPO/'scripts/lab/blender/run.sh'),'compose-views','--mode','url-images','--source-step','hf-right,hf-back,hf-left','--include-reference','references/views/candidate-0.png','--expected-count','4','--size','32'],env=env,capture_output=True)
            self.assertEqual(r.returncode,0,r.stderr.decode());record=json.loads((compose/'provenance.json').read_text());self.assertEqual(len(record['sources']),4);self.assertEqual(len(record['candidates']),4)
if __name__=='__main__':unittest.main()
