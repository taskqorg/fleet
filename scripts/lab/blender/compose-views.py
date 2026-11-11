"""Retain prepared image bytes, then normalize declared sheet cells or separate images.
Semantic provider ordering is deliberately absent; a separate inspection supplies view-map.json.
"""
import argparse, base64, hashlib, json, math, os, pathlib, sys, urllib.request
import bpy

parser = argparse.ArgumentParser()
parser.add_argument('--mode', choices=['fixture-sheet', 'gemini-sheet', 'url-images'], required=True)
parser.add_argument('--source-step', default='prep')
parser.add_argument('--include-reference', help='Retained relative reference path selected by the already verified reference encoder')
parser.add_argument('--reference-map', action='store_true', help='Use an explicit source-hash-bound canonical fixture inspection, never provider ordering')
parser.add_argument('--grid', default='3,1')
parser.add_argument('--size', type=int, default=1024)
parser.add_argument('--padding', type=float, default=.05)
parser.add_argument('--expected-count', type=int, default=3)
args = parser.parse_args(sys.argv[sys.argv.index('--')+1:])
root, out = pathlib.Path(os.environ['FLEET_INPUT_DIR']), pathlib.Path(os.environ['FLEET_OUTPUT_DIR'])
(out/'raw').mkdir(parents=True, exist_ok=True)
(out/'views').mkdir(exist_ok=True)
record = {'mode': args.mode, 'size': args.size, 'padding': args.padding, 'grid': args.grid,
          'expected_count': args.expected_count, 'sources': [], 'candidates': [], 'state': 'preparing'}
def retain():
    (out/'provenance.json').write_text(json.dumps(record, indent=2)+'\n')
def digest(data):
    return hashlib.sha256(data).hexdigest()
def media(data):
    if data.startswith(b'\x89PNG\r\n\x1a\n'): return 'image/png'
    if data.startswith(b'\xff\xd8\xff'): return 'image/jpeg'
    if data.startswith(b'RIFF') and data[8:12] == b'WEBP': return 'image/webp'
    raise ValueError('unsupported decoded source format')
def normalize(image, bounds, index, source):
    x0,y0,x1,y1 = bounds
    width,height = x1-x0,y1-y0
    pixels = list(image.pixels)
    cropped = []
    for y in range(y0,y1):
        for x in range(x0,x1):
            offset = (y*image.size[0]+x)*4
            rgba = pixels[offset:offset+4]
            cropped.extend([rgba[c]*rgba[3]+1-rgba[3] for c in range(3)]+[1])
    working = bpy.data.images.new('cell-'+str(index), width=width, height=height, alpha=False)
    working.pixels[:] = cropped
    factor = args.size/(max(width,height)*(1+2*args.padding))
    target_w,target_h = max(1,round(width*factor)),max(1,round(height*factor))
    working.scale(target_w,target_h)
    resized = list(working.pixels)
    square = [1.]*(args.size*args.size*4)
    dx,dy = (args.size-target_w)//2,(args.size-target_h)//2
    for y in range(target_h):
        start=((dy+y)*args.size+dx)*4
        square[start:start+target_w*4]=resized[y*target_w*4:(y+1)*target_w*4]
    result=bpy.data.images.new('normalized-'+str(index),width=args.size,height=args.size,alpha=False)
    result.pixels[:]=square
    result.filepath_raw=str(out/'views'/('candidate-'+str(index)+'.png'))
    result.file_format='PNG'; result.save()
    data=pathlib.Path(result.filepath_raw).read_bytes()
    record['candidates'].append({'index':index,'source_index':source['index'],'source_sha256':source['sha256'],
        'crop_pixels':[x0,y0,x1,y1],'path':'views/candidate-'+str(index)+'.png','sha256':digest(data),
        'width':args.size,'height':args.size,'media_type':'image/png'})
    bpy.data.images.remove(working);bpy.data.images.remove(result)

try:
    if args.size <= 0 or args.expected_count <= 0 or not math.isfinite(args.padding) or args.padding < 0:
        raise ValueError('size/count must be positive and padding finite/nonnegative')
    sources=[]
    if args.mode=='fixture-sheet':
        sources=[((root/'example'/'turnaround.png').read_bytes(),'example/turnaround.png')]
    else:
        if args.mode=='url-images':
            urls=[]
            if args.include_reference:
                path=(root/args.include_reference).resolve()
                if not path.is_relative_to(root.resolve()): raise ValueError('reference path escapes retained inputs')
                data=path.read_bytes();sources.append((data,'retained-reference:'+args.include_reference))
                target=out/'raw'/'source-0.bin';target.write_bytes(data)
                record['sources'].append({'index':0,'source':'retained-reference:'+args.include_reference,'path':'raw/'+target.name,'sha256':digest(data),'bytes':len(data)});retain()
            for step in args.source_step.split(','):
                reply=json.loads((root/step/'output.json').read_text())
                listed=reply.get('image_urls',reply.get('receipt',{}).get('image_urls'))
                if listed is None: listed=[image['url'] for image in reply.get('images',[])]
                if not isinstance(listed,list): raise ValueError('prepared image URLs must be an array')
                urls.extend(listed)
            for url in urls:
                if not isinstance(url,str): raise ValueError('prepared image URL must be a string')
                if url.startswith('data:image/') and ';base64,' in url:
                    data=base64.b64decode(url.split(',',1)[1],validate=True);source_name='data-uri:sha256:'+digest(url.encode())
                elif url.startswith('https://'):
                    with urllib.request.urlopen(url,timeout=300) as response:data=response.read()
                    source_name=url
                else:raise ValueError('prepared view must be HTTPS or a base64 image data URI')
                index=len(sources);sources.append((data,source_name))
                path=out/'raw'/('source-'+str(index)+'.bin');path.write_bytes(data)
                record['sources'].append({'index':index,'source':source_name,'path':'raw/'+path.name,'sha256':digest(data),'bytes':len(data)});retain()
        else:
            reply=json.loads((root/args.source_step/'output.json').read_text())
            for candidate in reply.get('candidates',[]):
                for part in candidate.get('content',{}).get('parts',[]):
                    inline=part.get('inlineData',part.get('inline_data',{}))
                    if inline.get('data'): sources.append((base64.b64decode(inline['data'],validate=True),'gemini:inline-image'))
    if args.mode!='url-images':
        for index,(data,source) in enumerate(sources):
            path=out/'raw'/('source-'+str(index)+'.bin');path.write_bytes(data)
            record['sources'].append({'index':index,'source':source,'path':'raw/'+path.name,'sha256':digest(data),'bytes':len(data)})
            retain()
    expected_sources=args.expected_count if args.mode=='url-images' else 1
    if len(sources)!=expected_sources: raise ValueError('expected '+str(expected_sources)+' sources, retained '+str(len(sources)))
    cols,rows=[int(x) for x in args.grid.split(',')]
    if cols<=0 or rows<=0 or args.mode!='url-images' and cols*rows!=args.expected_count:
        raise ValueError('sheet grid must match declared candidate count')
    for source in record['sources']:
        data=(out/source['path']).read_bytes(); source['media_type']=media(data)
        image=bpy.data.images.load(str(out/source['path']),check_existing=False)
        width,height=image.size;source.update(width=width,height=height)
        if width<=0 or height<=0: raise ValueError('source decoder returned no dimensions')
        if args.mode=='url-images': normalize(image,(0,0,width,height),len(record['candidates']),source)
        else:
            if width%cols or height%rows: raise ValueError('sheet dimensions do not divide declared grid')
            for row in range(rows):
                for col in range(cols):
                    # Cell indices are reading order, while Blender pixel rows begin at the bottom.
                    normalize(image,(col*width//cols,(rows-row-1)*height//rows,(col+1)*width//cols,(rows-row)*height//rows),len(record['candidates']),source)
        bpy.data.images.remove(image);retain()
    record['state']='ready-for-mapping';retain()
    if args.reference_map:
        if args.mode!='fixture-sheet': raise ValueError('canonical reference mapping applies only to the input fixture')
        guide=json.loads((root/'example'/'rig-guide.json').read_text())['reference_sheet']
        if guide['sha256']!=record['sources'][0]['sha256'] or len(guide['views'])!=len(record['candidates']):
            raise ValueError('canonical fixture inspection does not bind the actual reference bytes/count')
        mapped=[]
        for candidate,inspection in zip(record['candidates'],guide['views']):
            if inspection.get('basis') not in ['operator-verified','agent-inspected'] or not inspection.get('rationale'):
                raise ValueError('canonical reference inspection is unavailable')
            mapped.append({'candidate_index':candidate['index'],'source_sha256':candidate['source_sha256'],
                'sha256':candidate['sha256'],'view':inspection['view'],'basis':inspection['basis'],'rationale':inspection['rationale']})
        (out/'view-map.json').write_text(json.dumps({'views':mapped},indent=2)+'\n')
    (out/'output.json').write_text(json.dumps(record)+'\n')
except Exception as error:
    record['state']='failed';record['error']=str(error);retain()
    raise
