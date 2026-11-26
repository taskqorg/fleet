"""Step 2 of the board's component masters (step 1: board-extract.mjs). Turns the book JSON into Brilliant html fences:
each book section becomes one component master (comp) in board/components.bl, every colour bound to a Fleet token
by its dark and light value pair, so a master follows both themes. Writes comp<n>.txt (ten masters each, under the
5000-element ceiling of one edit) into the output folder; send each with Brilliant's edit(textFile=...) from the board
home, after deleting the batch frame it replaces.
Run: python3 packages/ui/scripts/board-components.py /tmp/book.json /tmp/board-components
"""
import json,re,sys,html,os
ROOT=os.path.join(os.path.dirname(os.path.abspath(__file__)),"..","..","..")
ds=open(os.path.join(ROOT,"docs/design-system/board/Styles/default.ds")).read()
tok={}
pair={}
for name,light,dark in re.findall(r'^(fleet\.[a-z0-9-]+): color\((#[0-9a-fA-F]{6}), dark: (#[0-9a-fA-F]{6})\)',ds,re.M):
    tok.setdefault(dark.lower(),name); pair.setdefault((dark.lower(),light.lower()),name)
def rgb(s):
    m=re.match(r'rgba?\(([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?\)',s or '')
    if not m: return None
    a=float(m.group(4)) if m.group(4) else 1
    return (int(float(m.group(1))),int(float(m.group(2))),int(float(m.group(3))),a)
def hexof(c): return '#%02x%02x%02x'%c[:3]
stats={'tok':0,'near':0,'bare':0}
def token(s,l=None):
    c=rgb(s)
    if not c or c[3]==0: return None,None
    h=hexof(c)
    lc=rgb(l) if l else None
    if lc and (h,hexof(lc)) in pair: stats['pair']=stats.get('pair',0)+1; return pair[(h,hexof(lc))],h
    if h in tok: stats['tok']+=1; return tok[h],h
    best=min(tok,key=lambda t:sum((int(t[i:i+2],16)-c[(i-1)//2])**2 for i in (1,3,5)))
    d=sum((int(best[i:i+2],16)-c[(i-1)//2])**2 for i in (1,3,5))
    if d<=48: stats['near']+=1; return tok[best],best
    stats['bare']+=1; return None,h
SP={2:'xs',4:'sm',8:'md',12:'lg',16:'xl',24:'2xl',32:'3xl'}
def px(v):
    try: return float(v.replace('px',''))
    except: return 0
MASTERS=False  # board-masters.py: block children fill their parent, buttons hug, the root keeps its width
PH=[]  # nested atoms of the masters page: (placeholder id, master, at, texts), board-masters.py turns them into instances
def node(n,ts,root_attrs=''):
    if 'inst' in n:
        pid=f"ph{len(PH)}"; PH.append((pid,n['inst'],n['at'],n.get('texts',[])))
        return f'<div id="{pid}" style="width:{n["w"]}px;height:{n["h"]}px;flex-shrink:0"></div>'
    if 'text' in n:
        raw=n['text']; t=html.escape(raw)
        if raw.startswith(' '): t='&nbsp;'+t[1:]
        if raw.endswith(' ') and len(raw)>1: t=t[:-1]+'&nbsp;'
        style=f"font-size:{ts['font-size']};font-weight:{ts['font-weight']};color:{ts['hex'] or '#ececec'}"
        if ts['mono']: style+=";font-family:'JetBrains Mono'"
        if ts.get('italic'): style+=";font-style:italic"
        db=f' data-brilliant="f[($' + ts['tok'] + ')]"' if ts['tok'] else ''
        nm=f' data-id="{html.escape(n["name"])}"' if n.get('name') else ''
        if MASTERS and ts.get('wrap'): style+=';width:100%'
        return f'<span style="{style}"{db}{nm}>{t}</span>'
    s=n['s']
    if n.get('svg'):
        L=n.get('l',{}); itok,_=token(s.get('color',''),L.get('color')) if MASTERS else (None,None)
        nm=f' data-id="{html.escape(n["name"] + ("~" + itok if itok else ""))}"' if n.get('name') else ''
        svg=re.sub(r'<svg','<svg%s width="%d" height="%d"'%(nm,max(1,round(n['w'])),max(1,round(n['h']))),n['svg'],count=1)
        return svg
    if n.get('img'):
        return f'<div style="width:{n["w"]}px;height:{n["h"]}px;background:#232326" data-brilliant="f[($fleet.neutral-bg)]"></div>'
    L=n.get('l',{})
    ctok,chex=token(s['color'],L.get('color'))
    ts2={'font-size':s['font-size'],'font-weight':s['font-weight'],'tok':ctok,'hex':chex,'italic':s.get('font-style')=='italic','mono':'mono' in s['font-family'].lower() or 'Mono' in s['font-family']}
    st=[];db=[]
    disp=s['display']
    flex='flex' in disp or 'grid' in disp
    st.append('display:flex')
    if 'grid' in disp: st.append('flex-wrap:wrap;flex-direction:row')
    elif 'flex' in disp:
        st.append('flex-direction:'+s['flex-direction'])
        if s['flex-wrap']=='wrap': st.append('flex-wrap:wrap')
    else:
        inline_only=all('text' in k or (k.get('s',{}).get('display','').startswith('inline')) for k in n['c'])
        st.append('flex-direction:'+('row' if inline_only else 'column'))
    for k in ('align-items','justify-content'):
        v=s[k]
        if v and v not in ('normal','stretch'): st.append(f'{k}:{v}')
    g=s['gap'].split(' ')[0] if s['gap'] and s['gap']!='normal' else ''
    if g: st.append('gap:'+g)
    pads=[s['padding-top'],s['padding-right'],s['padding-bottom'],s['padding-left']]
    if any(px(p) for p in pads): st.append('padding:'+' '.join(pads))
    bt,bth=token(s['background-color'],L.get('bg'))
    if bt: db.append(f'f[(${bt})]')
    elif bth: st.append('background:'+bth)
    bw=px(s['border-top-width']) if s['border-top-style']!='none' else 0
    sides=[px(s[f'border-{x}-width']) for x in ('top','right','bottom','left')]
    if any(sides):
        k=[x for x,w in zip(('top','right','bottom','left'),sides) if w][0]
        lt,lh=token(s[f'border-{k}-color'],L.get('border') if k!='left' else L.get('borderL'))
        if MASTERS and not lt and not lh: sides=[0,0,0,0]
    if any(sides):
        if all(sides): st.append(f'border:{sides[0]}px solid {lh or "#2a2a2d"}')
        if lt and all(sides): db.append(f'st[(${lt},w({int(sides[0])}))]')
        elif lt: db.append('st[($'+lt+',w('+','.join(str(int(w)) for w in sides)+'))]')
        elif not all(sides):
            for x,w in zip(('top','right','bottom','left'),sides):
                if w: st.append(f'border-{x}:{w}px solid {lh or "#2a2a2d"}')
    if px(s['border-radius']): st.append('border-radius:'+s['border-radius'].split(' ')[0])
    if s['opacity'] not in ('1',''): st.append('opacity:'+s['opacity'])
    leaf=all('text' in k or k.get('svg') for k in n['c'])
    boxed=bool(bth) or bool(bt) or any(sides)
    grow=MASTERS and n.get('_pdir')=='row' and (px(s.get('flex-grow','0'))>0 or (n.get('_pw') and abs(n['w']-n['_pw'])<=1))
    stretch=MASTERS and n.get('_pdir')=='column' and n.get('_pw') and abs(n['w']-n['_pw'])<=1
    badge=MASTERS and boxed and n['w']<=96 and abs(n['w']-n['h'])<=1 and s['align-items']=='center'
    if badge: st.append(f'width:{n["w"]}px;height:{n["h"]}px;flex-shrink:0')
    elif MASTERS and root_attrs and n.get('_fixed'):
        st.append(f'width:{n["w"]}px')
    elif grow: st.append('flex:1 1 0')
    elif stretch: st.append('align-self:stretch')
    elif not flex and n['tag'] in ('div','section','ul','li','table','tr','td','th','tbody','thead','article','header','footer','nav','main','form','fieldset'):
        st.append(f'width:{n["w"]}px'); db.append(f's({n["w"]},hug)')
    elif (n['tag'] in ('input','textarea','select') or s['overflow']!='visible') or (n['tag']=='button' and not MASTERS):
        st.append(f'width:{n["w"]}px')
    if not n['c'] and not any(x.startswith('width:') for x in st): st.append(f'width:{n["w"]}px;height:{n["h"]}px;flex-shrink:0'); db.append(f's({n["w"]},{n["h"]})')
    ts2['wrap']=MASTERS and leaf and len(n['c'])==1 and 'text' in n['c'][0] and s.get('white-space','normal')=='normal' and (grow or stretch or n.get('_fixed'))
    if (leaf and n['c'] and not ts2['wrap']) or (boxed and n['h']<=44): st.append(f'height:{n["h"]}px')
    if 'inline' in disp and 'flex' in disp and 'align-items:' not in ';'.join(st): st.append('align-items:center')
    if 'grid' in disp and not g:
        rg=s['row-gap'] if s['row-gap'] not in ('normal','') else ''
        if rg: st.append('gap:'+rg)
    pdir='column' if 'flex-direction:column' in ';'.join(st) else 'row'
    inner=n['w']-px(s['padding-left'])-px(s['padding-right'])-(px(s['border-left-width'])+px(s['border-right-width']) if any(sides) else 0)
    for k in n['c']:
        if 'text' not in k: k['_pdir']=pdir; k['_pw']=inner
    kids=''.join(node(k,ts2) for k in n['c'])
    attr=f' data-brilliant="{" ".join(db)}"' if db else ''
    return f'<div{root_attrs} style="{";".join(st)}"{attr}>{kids}</div>'
if __name__=='__main__':
  src,outdir=sys.argv[1],sys.argv[2]
  data=json.load(open(src))
  only=sys.argv[3:]
  out=[]
  for i,sec in enumerate(data):
      if only and sec['id'] not in only: continue
      name=sec['title'] or sec['id']
      kids=''.join(node(k,{'font-size':'13px','font-weight':'400','tok':'fleet.fg','hex':'#ececec','mono':False}) for k in sec['kids'])
      body=f'<div data-id="{html.escape(name)}" style="display:flex;flex-direction:column;gap:12px;padding:16px;width:{sec["w"]}px;background:#121213" data-brilliant="comp f[($fleet.surface)] st[($fleet.line,w(1))]">{kids}</div>'
      out.append((sec['id'],name,body))

  g=[{'id':a,'name':b,'html':c} for a,b,c in out if a not in ('tokens','navigation')]
  FENCE='`'*3
  for i in range(0,len(g),10):
      b=g[i:i+10]; n=i//10+1; names=', '.join(x['name'] for x in b).replace('"','')[:120]
      body=''.join(x['html'] for x in b)
      open(os.path.join(outdir,f'comp{n}.txt'),'w').write(f'{FENCE}html\nfile("components")\n<div data-id="Components {n} · {names}" style="display:flex;flex-direction:column;gap:24px;padding:32px;width:1400px;background:#0b0b0c" data-brilliant="ds(, theme(dark)) f[($fleet.bg)]">{body}</div>\n{FENCE}\n')

  print(len(out),stats,sum(len(c) for _,_,c in out))
