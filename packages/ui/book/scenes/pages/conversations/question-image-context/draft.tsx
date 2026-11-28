import { createSignal, onCleanup, Show } from 'solid-js';
import { Button, TabBar, QuestionCard, DecisionBlock, PanelSheet, QuestionImageViewer } from './ui.ts';

import { image } from './fixture.ts';
const question=`The team has prepared a conversation view for the project assistant. Please review whether the information is easy to read and the main actions are clear.\n\nThe assistant gives a concise summary of work in progress. It should be easy to read the latest message, understand what is waiting, and continue the conversation without losing the surrounding context.\n\nThe header identifies the assistant and the current project. The transcript keeps messages in a clear sequence, and the composer remains available below the conversation.\n\nLong task names and messages should wrap naturally. Dates and states should remain distinct, while secondary details stay quiet until they are needed.\n\nOn a phone, the conversation should give enough space to the message and keep the input within reach. The navigation and actions should remain understandable without relying on hover.\n\nThe attached preview shows a typical conversation after the assistant has reported the project status. It includes a question from the person, a response from the assistant, and a place to write the next message.\n\nPlease inspect the example closely. Check that the heading, message text and Send action are legible, and that the contrast makes the different parts easy to distinguish.\n\nThe assistant's reply should make the number of active tasks easy to find. Supporting detail can remain secondary, but it should still be readable when needed.\n\nA person may open this review from the conversation itself or from the related task. In both places they need access to the same image and the same answer choices.\n\nAfter viewing the preview, return to this question to share whether it is readable or needs adjustment.`;
const options=[{label:'Readable',primary:true},{label:'Needs adjustment'}];
export default function Scene(){
 type ReturnPoint={surface:'conversation'|'task';theme:'dark'|'light';scroll:number};
 const returnPoint=():ReturnPoint|undefined=>{
   const value=history.state?.questionImageReturn as ReturnPoint|undefined;
   if(!value||(value.surface!=='conversation'&&value.surface!=='task')||
      (value.theme!=='dark'&&value.theme!=='light')||!Number.isFinite(value.scroll)||value.scroll<0)return;
   return value;
 };
 const initial=returnPoint();
 const [surface,setSurface]=createSignal<'conversation'|'task'>(initial?.surface??'conversation');const [blocked,setBlocked]=createSignal(false);const [view,setView]=createSignal(false);const [theme,setTheme]=createSignal<'dark'|'light'>(initial?.theme??'dark');let trigger:HTMLElement|undefined;let scroller:HTMLDivElement|undefined;let saved=0;let enteredFullscreen=false;let closing=false;
 const rememberOriginal=()=>{
   // The preview's opaque origin forbids storage and new tabs. State on this
   // history entry survives an ordinary Back even when the document reloads.
   history.replaceState({...history.state,questionImageReturn:{surface:surface(),theme:theme(),scroll:scroller?.scrollTop??0}},'');
 };
 const restoreOriginal=()=>{
   const point=returnPoint();
   if(!point)return;
   setSurface(point.surface);
   setTheme(point.theme);
   requestAnimationFrame(()=>requestAnimationFrame(()=>{
     if(scroller)scroller.scrollTop=point.scroll;
     scroller?.querySelector<HTMLAnchorElement>('.original-link')?.focus({preventScroll:true});
     const state={...history.state};
     delete state.questionImageReturn;
     history.replaceState(state,'');
   }));
 };
 window.addEventListener('pageshow',restoreOriginal);
 onCleanup(()=>window.removeEventListener('pageshow',restoreOriginal));
 if(initial&&document.readyState==='complete')restoreOriginal();

 const onFullscreen=()=>{if(enteredFullscreen&&!document.fullscreenElement&&!closing)close()};
 document.addEventListener('fullscreenchange',onFullscreen);
 onCleanup(()=>document.removeEventListener('fullscreenchange',onFullscreen));

 const open=(e:MouseEvent)=>{
   e.preventDefault();
   trigger=e.currentTarget as HTMLElement;
   saved=scroller?.scrollTop??0;
   setBlocked(false);
   const embedded=window.self!==window.top;
   const refuse=()=>{
     setBlocked(true);
     requestAnimationFrame(()=>document.querySelector('.embed-warning')?.scrollIntoView({block:'nearest'}));
   };
   const show=()=>{
     setView(true);
     requestAnimationFrame(()=>document.querySelector<HTMLElement>('.qiv-stage')?.focus({preventScroll:true}));
   };
   if(!document.fullscreenEnabled||!document.documentElement.requestFullscreen){
     if(embedded)refuse();
     else show();
     return;
   }
   // Ask from the originating gesture. The modal is promoted after the
   // fullscreen document, which keeps it at the top of Chromium's top layer.
   document.documentElement.requestFullscreen().then(()=>{
     enteredFullscreen=true;
     show();
   }).catch(()=>{
     if(embedded)refuse();
     else show();
   });
 };
 const close=()=>{
   if(!view()||closing)return;
   closing=true;
   setView(false);
   enteredFullscreen=false;
   const exit=document.fullscreenElement ? document.exitFullscreen().catch(()=>{}) : Promise.resolve();
   void exit.then(()=>requestAnimationFrame(()=>{
     if(scroller)scroller.scrollTop=saved;
     trigger?.focus({preventScroll:true});
     closing=false;
   }));
 };
 const context=()=> <div class="sample-attachment"><a class="attachment-thumbnail" href="#preview" aria-label="Open image preview" onClick={open}><img src={image} alt=""/></a><div class="attachment-copy"><strong>Conversation preview</strong><span>Example image · 1600 × 1000</span></div><Button label="Open image" icon="maximize-2" onClick={open}/><Show when={window.self===window.top}><a class="original-link" onClick={rememberOriginal} href="https://fleet.ufobe.com/api/results/f5402dd5-e95e-487e-8e28-b0f08c2d2b88">Open original PNG</a></Show><Show when={blocked()}><div class="embed-warning" role="status">This embedded preview cannot use the full screen. Use the original PNG link in the owner review report.</div></Show></div>;
 return <div class={['demo',`th-${theme()}`]}><div class="demo-bar"><span>Preview</span><TabBar label="Example location" tabs={[{id:'conversation',label:'Conversation'},{id:'task',label:'Task details'}]} value={surface()} onChange={v=>{setSurface(v);setView(false)}}/><Button label={theme()==='dark'?'Light':'Dark'} kind="quiet" onClick={()=>setTheme(theme()==='dark'?'light':'dark')}/></div>
 <div class="shell"><main class="work"><div class="shell-head"><strong>{surface()==='conversation'?'Conversation':'Task details'}</strong><span>Fleet</span></div><div class="workbody">
 <section class="question-surface"><header class="context-head"><div class="avatar">F</div><div><strong>{surface()==='conversation'?'Project assistant':'Review conversation details'}</strong><small>{surface()==='conversation'?'Fleet · conversation':'Fleet · task'}</small></div><span class="context-tag">Waiting for you</span></header><div class="question-scroll" ref={el=>scroller=el}>
 <Show when={surface()==='conversation'} fallback={<DecisionBlock question={question} askedBy="Project assistant" addressedTo="You" since={1795850000000} now={1795853600000} options={options} context={context()} onAnswer={()=>{}}/>}><QuestionCard question={question} choices={options} context={context()} onAnswer={()=>{}}/></Show>
 <div class="following"><strong>{surface()==='conversation'?'Conversation':'Activity'}</strong><p>The request is ready for your review. You can inspect the example above and return here without losing your place.</p></div></div></section>
 <PanelSheet open={view()} label="Image viewer" onClose={close}><Show when={view()}><QuestionImageViewer src={image} title="Conversation preview" onClose={close}/></Show></PanelSheet>
 </div></main></div></div>
}
