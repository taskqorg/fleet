"""Retain prepared image bytes, then normalize declared sheet cells or separate images.
Semantic provider ordering is deliberately absent; a separate inspection supplies view-map.json.
"""
import argparse, base64, hashlib, json, math, os, pathlib, sys, urllib.request
import bpy

parser = argparse.ArgumentParser()
parser.add_argument('--mode', choices=['fixture-sheet', 'gemini-sheet', 'url-images'], required=True)
parser.add_argument('--source-step', default='prep')
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
        reply=json.loads((root/args.source_step/'output.json').read_text())
        if args.mode=='url-images':
            urls=reply.get('image_urls',reply.get('receipt',{}).get('image_urls',[]))
            if not isinstance(urls,list): raise ValueError('image_urls must be an array')
            for index,url in enumerate(urls):
                if not isinstance(url,str) or not url.startswith('https://'): raise ValueError('prepared view URL must be HTTPS')
                with urllib.request.urlopen(url,timeout=300) as response: data=response.read()
                sources.append((data,url))
                path=out/'raw'/('source-'+str(index)+'.bin');path.write_bytes(data)
                record['sources'].append({'index':index,'source':url,'path':'raw/'+path.name,'sha256':digest(data),'bytes':len(data)})
                retain()
        else:
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
    (out/'output.json').write_text(json.dumps(record)+'\n')
except Exception as error:
    record['state']='failed';record['error']=str(error);retain()
    raise
