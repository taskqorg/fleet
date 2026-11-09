import {cpSync,mkdirSync} from 'node:fs';
mkdirSync('dist/config/agents',{recursive:true});
cpSync('config/agents','dist/config/agents',{recursive:true});
mkdirSync('dist/contracts',{recursive:true});
cpSync('contracts/lab-config.json','dist/contracts/lab-config.json');

for(const name of ['projects','pipelines'])cpSync('config/'+name,'dist/config/'+name,{recursive:true});
cpSync('config/catalogue.json','dist/config/catalogue.json');
cpSync('packages/coordinator/pipe_search.py','dist/packages/coordinator/pipe_search.py');

cpSync('packages/coordinator/delivery_worker.py','dist/packages/coordinator/delivery_worker.py');
