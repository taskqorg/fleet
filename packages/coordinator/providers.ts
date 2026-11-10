// Provider catalogue (docs/designs/fleet-connections.md §1): code, not a table. The coordinator copies it into the gateway
// snapshot, so the gateway has no second copy. unit: what the provider's usage counts; idempotent: a call that got no answer
// may be sent again with another key.
export type Tool={method:'POST'|'GET',path:string,unit:string,idempotent:boolean};
export const providers:Record<string,{baseUrl:string,tools:Record<string,Tool>}>={
 tavily:{baseUrl:'https://api.tavily.com',tools:{
  web_search:{method:'POST',path:'/search',unit:'credits',idempotent:true},
  web_extract:{method:'POST',path:'/extract',unit:'credits',idempotent:true}}},
};
