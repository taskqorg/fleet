// Plays the executor's reply signal (go/executor/wake.go): a test that writes a journal reply itself signals every
// FIFO a waiting MCP server made under <journal dir>/wake.
import {readdirSync,openSync,writeSync,closeSync,constants} from 'node:fs';
import {join} from 'node:path';

export function wakeAll(journalDir:string){
 let fifos:string[]=[];try{fifos=readdirSync(join(journalDir,'wake'))}catch{}
 for(const f of fifos){try{const fd=openSync(join(journalDir,'wake',f),constants.O_WRONLY|constants.O_NONBLOCK);try{writeSync(fd,'x')}finally{closeSync(fd)}}catch{}}
}
