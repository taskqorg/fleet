#!/usr/bin/env python3
"""Independently bind inspected semantic views to retained bytes before a charged request."""
import argparse, base64, hashlib, json, os, pathlib, struct, zlib


def read_inside(root, relative):
    path=(root/relative).resolve()
    if not path.is_relative_to(root.resolve()): raise ValueError('view path escapes its retained input directory')
    return path.read_bytes()


def png_dimensions(data):
    if data[:8]!=b'\x89PNG\r\n\x1a\n': raise ValueError('normalized view is not PNG')
    position=8; chunks=[]; compressed=bytearray(); dimensions=None; ended=False
    while position<len(data):
        if len(data)-position<12: raise ValueError('truncated PNG chunk')
        size=struct.unpack('>I',data[position:position+4])[0]
        kind=data[position+4:position+8];body=data[position+8:position+8+size]
        if len(body)!=size or position+12+size>len(data): raise ValueError('truncated PNG data')
        crc=struct.unpack('>I',data[position+8+size:position+12+size])[0]
        if zlib.crc32(kind+body)&0xffffffff!=crc: raise ValueError('PNG CRC mismatch')
        if not chunks and kind!=b'IHDR': raise ValueError('PNG must start with IHDR')
        if kind==b'IHDR':
            if dimensions is not None or size!=13: raise ValueError('invalid PNG header')
            width,height,depth,color,compression,filtering,interlace=struct.unpack('>IIBBBBB',body)
            if width<=0 or height<=0 or (depth,color,compression,filtering,interlace)!=(8,2,0,0,0):
                raise ValueError('normalized view must be noninterlaced RGB8 PNG')
            dimensions=(width,height)
        elif kind==b'IDAT': compressed.extend(body)
        elif kind==b'tRNS': raise ValueError('normalized view must be opaque')
        elif kind==b'IEND':
            if size or position+12!=len(data): raise ValueError('invalid PNG end')
            ended=True;break
        elif kind[:1].isupper() and kind not in (b'PLTE',): raise ValueError('unsupported critical PNG chunk')
        chunks.append(kind);position+=size+12
    if not ended or dimensions is None or not compressed: raise ValueError('incomplete PNG')
    width,height=dimensions
    decoder=zlib.decompressobj();pixels=decoder.decompress(compressed,(width*3+1)*height+1)
    if not decoder.eof or decoder.unused_data or len(pixels)!=(width*3+1)*height: raise ValueError('invalid decoded PNG byte count')
    if any(pixels[row*(width*3+1)]>4 for row in range(height)): raise ValueError('invalid PNG filter')
    return dimensions


def encode_views(root, provenance, mapping, expected):
    if len(expected)!=len(set(expected)) or not expected: raise ValueError('expected view names must be nonempty and unique')
    candidates=provenance.get('candidates',[]);views=mapping.get('views',[])
    if provenance.get('state')!='ready-for-mapping' or len(candidates)!=len(expected) or len(views)!=len(expected):
        raise ValueError('missing or extra views; all evidence remains retained')
    by_index={candidate['index']:candidate for candidate in candidates}
    if len(by_index)!=len(candidates): raise ValueError('duplicate candidate index')
    sources={source['index']:source for source in provenance['sources']}
    named={};seen=set()
    for view in views:
        index=view.get('candidate_index');name=view.get('view');candidate=by_index.get(index)
        if candidate is None or index in seen or name not in expected or name in named: raise ValueError('unknown, duplicate or ambiguous view mapping')
        if view.get('basis') not in ['operator-verified','agent-inspected'] or not str(view.get('rationale','')).strip():
            raise ValueError('view mapping needs an explicit inspection basis and rationale')
        source=sources.get(candidate['source_index'])
        if source is None: raise ValueError('missing retained source')
        raw=read_inside(root,source['path']);source_hash=hashlib.sha256(raw).hexdigest()
        if source_hash!=source['sha256'] or len(raw)!=source['bytes'] or source_hash!=candidate['source_sha256'] or source_hash!=view.get('source_sha256'):
            raise ValueError('source hash binding mismatch')
        data=read_inside(root,candidate['path']);actual=hashlib.sha256(data).hexdigest()
        if actual!=candidate['sha256'] or actual!=view.get('sha256'): raise ValueError('normalized view hash binding mismatch')
        if png_dimensions(data)!=(candidate['width'],candidate['height']) or candidate['media_type']!='image/png':
            raise ValueError('decoded view dimensions/media type mismatch')
        named[name]='data:image/png;base64,'+base64.b64encode(data).decode()
        seen.add(index)
    return [named[name] for name in expected]


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--compose-step',default='compose')
    parser.add_argument('--map-step',default='map')
    parser.add_argument('--expected',default='front,left,back')
    parser.add_argument('--request-kind',choices=['prep','multi','google','hf'],required=True)
    parser.add_argument('--prompt',default='')
    parser.add_argument('--max-request-bytes',type=int,required=True)
    args=parser.parse_args()
    root=pathlib.Path(os.environ['FLEET_INPUT_DIR']);out=pathlib.Path(os.environ['FLEET_OUTPUT_DIR'])
    provenance=json.loads((root/args.compose_step/'provenance.json').read_text())
    mapping=json.loads((root/args.map_step/'view-map.json').read_text())
    views=encode_views(root/args.compose_step,provenance,mapping,args.expected.split(','))
    if args.request_kind=='prep':
        if not args.prompt.strip(): raise ValueError('preparation prompt required')
        request={'reference_image_urls':views,'prompt':args.prompt,'ai_model':'nano-banana-2','generate_multi_view':True}
    elif args.request_kind=='google':
        parts=[{'text':args.prompt+' Produce one image with exactly three equal-width columns: front, left, back of the same character, no labels, full body, consistent A-pose, white background.'}]
        for name,uri in zip(args.expected.split(','),views):parts.extend([{'text':'Reference '+name+' view'},{'inlineData':{'mimeType':'image/png','data':uri.split(',',1)[1]}}])
        request={'model':'gemini-3.1-flash-image-preview','contents':[{'role':'user','parts':parts}],'generationConfig':{'responseModalities':['IMAGE']}}
    elif args.request_kind=='hf':
        request={'prompt':'<sks> right side view eye-level shot medium shot','image_urls':[views[args.expected.split(',').index('front')]],'loras':[{'path':'https://huggingface.co/fal/Qwen-Image-Edit-2511-Multiple-Angles-LoRA/resolve/main/qwen-image-edit-2511-multiple-angles-lora.safetensors','scale':.9}],'num_inference_steps':40,'guidance_scale':4.,'seed':42,'output_format':'png','sync_mode':True}
    else:
        request={'image_urls':views,'ai_model':'latest','should_remesh':True,'target_polycount':200000,'topology':'triangle','save_pre_remeshed_model':True,'should_texture':True,'enable_pbr':True,'pose_mode':'a-pose','target_formats':['glb']}
        if args.prompt.strip(): request['texture_prompt']=args.prompt
    body=json.dumps(request,separators=(',',':')).encode()
    if args.max_request_bytes<=0 or len(body)>args.max_request_bytes: raise ValueError('encoded request exceeds declared gateway body contract before paid submission')
    candidate_by_index={candidate['index']:candidate for candidate in provenance['candidates']}
    paths_by_view={view['view']:args.compose_step+'/'+candidate_by_index[view['candidate_index']]['path'] for view in mapping['views']}
    result={'request':request,'payload_bytes':len(body),'view_map':mapping,'paths_by_view':paths_by_view,'normalization':{'size':provenance['size'],'padding':provenance['padding'],'grid':provenance['grid']}}
    if args.request_kind=='hf':
        result['angle_requests']={name:{**request,'prompt':'<sks> '+name+' side view eye-level shot medium shot' if name!='back' else '<sks> back view eye-level shot medium shot'} for name in ['right','back','left']}
        if any(len(json.dumps(value,separators=(',',':')).encode())>args.max_request_bytes for value in result['angle_requests'].values()): raise ValueError('HF request exceeds declared gateway body contract before paid submission')
    (out/'output.json').write_text(json.dumps(result)+'\n')

if __name__=='__main__': main()
