// Per-agent resume and fork argv, ported as data from Orca
// src/shared/agent-launch/startup/agent-resume-argv.ts (getAgentResumeArgv, FORK_ARGV).
// Fleet harness names map onto Orca agent names; `antigravity` resumes with `agy --conversation`.
export type NativeSession={id:string,key?:'session_id'|'conversation_id',transcriptPath?:string,effort?:string};
const sessionKey:Record<string,'session_id'|'conversation_id'>={cursor:'conversation_id',antigravity:'conversation_id'};
const simple:Record<string,(id:string)=>string[]>={
 codebuddy:id=>['codebuddy','--resume',id],cursor:id=>['cursor-agent','--resume',id],codex:id=>['codex','resume',id],
 'qoder-cn':id=>['qoderclicn','--resume',id],'qwen-code':id=>['qwen','--resume',id],qoder:id=>['qodercli','--resume',id],
 gemini:id=>['gemini','--resume',id],antigravity:id=>['agy','--conversation',id],opencode:id=>['opencode','--session',id],
 opencode2:id=>['opencode2','--session',id],'mimo-code':id=>['mimo','--session',id],droid:id=>['droid','--resume',id],
 grok:id=>['grok','--resume',id],devin:id=>['devin','--resume',id],copilot:id=>['copilot',`--resume=${id}`],
 kimi:id=>['kimi','--session',id],muse:id=>['muse','resume',id],zcode:id=>['zcode','--resume',id],
 dsh:id=>['dsh-tui','--backend=dsh','--resume',id],jcode:id=>['jcode','--resume',id],kiro:id=>['kiro-cli','--resume-id',id],hermes:id=>['hermes','--resume',id]};
export const keyFor=(agent:string)=>sessionKey[agent]??'session_id';
export function resumeArgv(agent:string,s:NativeSession):string[]|null{
 if((s.key??keyFor(agent))!==keyFor(agent))return null;
 if(agent==='claude')return ['claude',...(s.effort?['--effort',s.effort]:[]),'--resume',s.id];
 if(agent==='pi')return s.transcriptPath?['pi','--session',s.transcriptPath]:null;
 if(agent==='prime-agent')return s.transcriptPath?['prime-agent','--resume',s.transcriptPath]:null;
 if(agent==='omp')return ['omp','--resume',s.transcriptPath?.trim()||s.id];
 return simple[agent]?.(s.id)??null;
}
// Native fork creates a new conversation id; the source stays untouched (Orca FORK_ARGV).
const fork:Record<string,(s:NativeSession)=>string[]|null>={
 claude:({id})=>['claude','--resume',id,'--fork-session'],codex:({id})=>['codex','fork',id],
 grok:({id})=>['grok','--resume',id,'--fork-session'],pi:({transcriptPath})=>transcriptPath?['pi','--fork',transcriptPath]:null};
export const forkArgv=(agent:string,s:NativeSession)=>fork[agent]?.(s)??null;
