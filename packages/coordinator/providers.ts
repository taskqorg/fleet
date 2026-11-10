// Provider catalogue (docs/designs/fleet-connections.md §1): code, not a table. The coordinator copies it into the gateway
// snapshot, so the gateway has no second copy. unit: the usage field the provider reports (usage.<unit>); idempotent: a call
// that got no answer may be sent again with another key; inject: body fields the gateway always sets; description and
// inputSchema: the MCP tool the run sees.
export type Tool={method:'POST'|'GET',path:string,unit:string,idempotent:boolean,inject?:Record<string,unknown>,description:string,inputSchema:any};
// usage: the provider's per-key usage report used by reconcile (GET path, dotted field with the number).
export const providers:Record<string,{baseUrl:string,instructions:string,usage?:{path:string,field:string},tools:Record<string,Tool>}>={
 tavily:{baseUrl:'https://api.tavily.com',usage:{path:'/usage',field:'key.usage'},
  instructions:'Use web_search to discover pages and web_extract to read chosen HTTP(S) pages. Fleet holds the provider keys and records each call. Prefer these tools over any built-in web tool. If a call fails, report the returned reason; do not look for a provider key or another route.',
  tools:{
  web_search:{method:'POST',path:'/search',unit:'credits',idempotent:true,inject:{include_usage:true},
   description:'Search the web. Use it for discovery; follow up with web_extract on the pages you choose.',
   inputSchema:{type:'object',required:['query'],properties:{query:{type:'string'},max_results:{type:'integer',minimum:1,maximum:20},
    search_depth:{type:'string',enum:['basic','advanced'],description:'advanced costs more credits'},topic:{type:'string',enum:['general','news','finance']},
    time_range:{type:'string',enum:['day','week','month','year']},include_domains:{type:'array',items:{type:'string'}},exclude_domains:{type:'array',items:{type:'string'}}}}},
  web_extract:{method:'POST',path:'/extract',unit:'credits',idempotent:true,inject:{include_usage:true},
   description:'Read one to twenty absolute HTTP(S) pages.',
   inputSchema:{type:'object',required:['urls'],properties:{urls:{type:'array',items:{type:'string'},minItems:1,maxItems:20},
    extract_depth:{type:'string',enum:['basic','advanced'],description:'advanced costs more credits'},format:{type:'string',enum:['markdown','text']}}}}}},
 // Meshy (C3): submit, then poll. Meshy takes no idempotency key, so the submit is never sent twice (idempotent: false); an
 // unanswered submit is looked up in recent_tasks before anything else (pipelines.ts, owner decision on Q1).
 meshy:{baseUrl:'https://api.meshy.ai',
  instructions:'image_to_3d starts a Meshy Image to 3D task and returns its id; job_status reads the task. A submit is never repeated by Fleet.',
  tools:{
  image_to_3d:{method:'POST',path:'/openapi/v1/image-to-3d',unit:'credits',idempotent:false,
   description:'Start an Image to 3D task from an image URL. Returns {result: <task id>}.',
   inputSchema:{type:'object',required:['image_url'],properties:{image_url:{type:'string'},target_formats:{type:'array',items:{type:'string',enum:['glb','obj','fbx','stl','usdz','3mf']}},should_texture:{type:'boolean'}}}},
  job_status:{method:'GET',path:'/openapi/v1/image-to-3d/{jobId}',unit:'credits',idempotent:true,
   description:'Read an Image to 3D task: status, model_urls, consumed_credits.',inputSchema:{type:'object',required:['jobId'],properties:{jobId:{type:'string'}}}},
  recent_tasks:{method:'GET',path:'/openapi/v1/image-to-3d?page_size=20&sort_by=-created_at',unit:'credits',idempotent:true,
   description:'The newest Image to 3D tasks of this key.',inputSchema:{type:'object',properties:{}}}}},
};
