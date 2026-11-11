// Provider catalogue (docs/designs/fleet-connections.md §1): code, not a table; the
// coordinator copies it into the gateway snapshot. Kept as the JSON the TypeScript coordinator produced.
package main

import (
	"encoding/json"

	"fleet/contract"
)

var providers = func() map[string]any {
	var p map[string]any
	if err := json.Unmarshal([]byte(providersJSON), &p); err != nil {
		panic(err)
	}
	return p
}()

// gatewayProviders: the same catalogue as the gateway reads it (contracts/gateway.schema.json GatewayProvider).
var gatewayProviders = func() map[string]contract.GatewayProvider {
	var p map[string]contract.GatewayProvider
	if err := json.Unmarshal([]byte(providersJSON), &p); err != nil {
		panic(err)
	}
	return p
}()

func providerTools(name string) doc { return mapOf(mapOf(providers[name])["tools"]) }

const providersJSON = `
{
 "tavily": {
  "baseUrl": "https://api.tavily.com",
  "usage": {
   "path": "/usage",
   "field": "key.usage"
  },
  "instructions": "Use web_search to discover pages and web_extract to read chosen HTTP(S) pages. Fleet holds the provider keys and records each call. Prefer these tools over any built-in web tool. If a call fails, report the returned reason; do not look for a provider key or another route.",
  "tools": {
   "web_search": {
    "method": "POST",
    "path": "/search",
    "unit": "credits",
    "idempotent": true,
    "inject": {
     "include_usage": true
    },
    "description": "Search the web. Use it for discovery; follow up with web_extract on the pages you choose.",
    "inputSchema": {
     "type": "object",
     "required": [
      "query"
     ],
     "properties": {
      "query": {
       "type": "string"
      },
      "max_results": {
       "type": "integer",
       "minimum": 1,
       "maximum": 20
      },
      "search_depth": {
       "type": "string",
       "enum": [
        "basic",
        "advanced"
       ],
       "description": "advanced costs more credits"
      },
      "topic": {
       "type": "string",
       "enum": [
        "general",
        "news",
        "finance"
       ]
      },
      "time_range": {
       "type": "string",
       "enum": [
        "day",
        "week",
        "month",
        "year"
       ]
      },
      "include_domains": {
       "type": "array",
       "items": {
        "type": "string"
       }
      },
      "exclude_domains": {
       "type": "array",
       "items": {
        "type": "string"
       }
      }
     }
    }
   },
   "web_extract": {
    "method": "POST",
    "path": "/extract",
    "unit": "credits",
    "idempotent": true,
    "inject": {
     "include_usage": true
    },
    "description": "Read one to twenty absolute HTTP(S) pages.",
    "inputSchema": {
     "type": "object",
     "required": [
      "urls"
     ],
     "properties": {
      "urls": {
       "type": "array",
       "items": {
        "type": "string"
       },
       "minItems": 1,
       "maxItems": 20
      },
      "extract_depth": {
       "type": "string",
       "enum": [
        "basic",
        "advanced"
       ],
       "description": "advanced costs more credits"
      },
      "format": {
       "type": "string",
       "enum": [
        "markdown",
        "text"
       ]
      }
     }
    }
   }
  }
 },
 "typesafe": {
  "baseUrl": "https://api.typesafe.ai",
  "instructions": "system_one asks Jev (TypeSafe) typed questions (noul, choice, score, box) about a state and returns one structured answer per question; list_models lists model names such as jev-latest. Docs: https://docs.typesafe.ai/llms.txt",
  "tools": {
   "system_one": {
    "method": "POST",
    "path": "/v1/systemone",
    "unit": "input_tokens",
    "headers": {
     "TypeSafe-Danger-Zone-Images": "true"
    },
    "idempotent": true,
    "description": "Answer questions about state. Body {model, state, questions: {<id>: {type, instructions, criteria?}}, assets?}. Returns answers, model and usage.",
    "inputSchema": {
     "type": "object",
     "required": [
      "model",
      "state",
      "questions"
     ],
     "properties": {
      "model": {
       "type": "string"
      },
      "state": {},
      "questions": {
       "type": "object"
      },
      "assets": {
       "type": "object"
      }
     }
    }
   },
   "list_models": {
    "method": "GET",
    "path": "/v1/models",
    "unit": "input_tokens",
    "idempotent": true,
    "description": "List models and aliases available to the key.",
    "inputSchema": {
     "type": "object",
     "properties": {}
    }
   }
  }
 },
 "meshy": {
  "baseUrl": "https://api.meshy.ai",
  "instructions": "image_to_3d and multi_image_to_3d start a Meshy task and return its id; job_status and multi_job_status read it. A submit is never repeated by Fleet.",
  "tools": {
   "image_to_3d": {
    "method": "POST",
    "path": "/openapi/v1/image-to-3d",
    "unit": "credits",
    "idempotent": false,
    "description": "Start an Image to 3D task from an image URL. Returns {result: <task id>}.",
    "inputSchema": {
     "type": "object",
     "required": [
      "image_url"
     ],
     "properties": {
      "image_url": {
       "type": "string"
      },
      "target_formats": {
       "type": "array",
       "items": {
        "type": "string",
        "enum": [
         "glb",
         "obj",
         "fbx",
         "stl",
         "usdz",
         "3mf"
        ]
       }
      },
      "should_texture": {
       "type": "boolean"
      },
      "ai_model": {
       "type": "string",
       "description": "meshy-4, meshy-5 or latest"
      },
      "topology": {
       "type": "string",
       "enum": [
        "quad",
        "triangle"
       ]
      },
      "target_polycount": {
       "type": "integer",
       "minimum": 100,
       "maximum": 300000
      },
      "enable_pbr": {
       "type": "boolean"
      },
      "texture_resolution": {
       "type": "string",
       "description": "for example 2k"
      }
     }
    }
   },
   "job_status": {
    "method": "GET",
    "path": "/openapi/v1/image-to-3d/{jobId}",
    "unit": "credits",
    "idempotent": true,
    "description": "Read an Image to 3D task: status, model_urls, consumed_credits.",
    "inputSchema": {
     "type": "object",
     "required": [
      "jobId"
     ],
     "properties": {
      "jobId": {
       "type": "string"
      }
     }
    },
    "pathPatterns": {
     "jobId": "^[0-9a-f-]+$"
    },
    "usageField": "consumed_credits",
    "usageWhen": {
     "field": "status",
     "equals": "SUCCEEDED"
    },
    "usageOnce": "jobId"
   },
   "recent_tasks": {
    "method": "GET",
    "path": "/openapi/v1/image-to-3d?page_size=20&sort_by=-created_at",
    "unit": "credits",
    "idempotent": true,
    "description": "The newest Image to 3D tasks of this key.",
    "inputSchema": {
     "type": "object",
     "properties": {}
    }
   },
   "multi_image_to_3d": {
    "method": "POST",
    "path": "/openapi/v1/multi-image-to-3d",
    "unit": "credits",
    "idempotent": false,
    "description": "Start a Multi-Image to 3D task from one to four views of one object. Returns {result: <task id>}.",
    "inputSchema": {
     "type": "object",
     "required": [
      "image_urls"
     ],
     "properties": {
      "image_urls": {
       "type": "array",
       "items": {
        "type": "string"
       },
       "minItems": 1,
       "maxItems": 4
      },
      "target_formats": {
       "type": "array",
       "items": {
        "type": "string",
        "enum": [
         "glb",
         "obj",
         "fbx",
         "stl",
         "usdz",
         "3mf"
        ]
       }
      },
      "should_texture": {
       "type": "boolean"
      },
      "ai_model": {
       "type": "string",
       "description": "meshy-4, meshy-5 or latest"
      },
      "topology": {
       "type": "string",
       "enum": [
        "quad",
        "triangle"
       ]
      },
      "target_polycount": {
       "type": "integer",
       "minimum": 100,
       "maximum": 300000
      },
      "enable_pbr": {
       "type": "boolean"
      },
      "texture_resolution": {
       "type": "string",
       "description": "for example 2k"
      }
     }
    }
   },
   "multi_job_status": {
    "method": "GET",
    "path": "/openapi/v1/multi-image-to-3d/{jobId}",
    "unit": "credits",
    "idempotent": true,
    "pathPatterns": {
     "jobId": "^[0-9a-f-]+$"
    },
    "description": "Read a Multi-Image to 3D task: status, model_urls, consumed_credits.",
    "inputSchema": {
     "type": "object",
     "required": [
      "jobId"
     ],
     "properties": {
      "jobId": {
       "type": "string"
      }
     }
    },
    "usageField": "consumed_credits",
    "usageWhen": {
     "field": "status",
     "equals": "SUCCEEDED"
    },
    "usageOnce": "jobId"
   },
   "multi_recent_tasks": {
    "method": "GET",
    "path": "/openapi/v1/multi-image-to-3d?page_size=20&sort_by=-created_at",
    "unit": "credits",
    "idempotent": true,
    "description": "The newest Multi-Image to 3D tasks of this key.",
    "inputSchema": {
     "type": "object",
     "properties": {}
    }
   }
  }
 },
 "huggingface": {
  "baseUrl": "https://router.huggingface.co",
  "instructions": "inference calls one model on Hugging Face Inference Providers (hf-inference); the body is the model's own request body plus model.",
  "tools": {
   "inference": {
    "method": "POST",
    "path": "/hf-inference/models/{model}",
    "unit": "requests",
    "idempotent": false,
    "pathPatterns": {
     "model": "^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$"
    },
    "description": "Run a model. Body {model: <owner>/<name>, inputs, parameters?}; the reply is the model's own answer.",
    "inputSchema": {
     "type": "object",
     "required": [
      "model"
     ],
     "properties": {
      "model": {
       "type": "string"
      },
      "inputs": {},
      "parameters": {
       "type": "object"
      }
     }
    }
   }
  }
 },
 "google-ai": {
  "baseUrl": "https://generativelanguage.googleapis.com",
  "keyHeader": "x-goog-api-key",
  "instructions": "generate_content calls a Gemini model; the body is the Gemini generateContent body plus model.",
  "tools": {
   "generate_content": {
    "method": "POST",
    "path": "/v1beta/models/{model}:generateContent",
    "unit": "tokens",
    "idempotent": false,
    "pathPatterns": {
     "model": "^[A-Za-z0-9._-]+$"
    },
    "usageField": "usageMetadata.totalTokenCount",
    "description": "Generate content. Body {model, contents, generationConfig?, systemInstruction?}; usage is usageMetadata.totalTokenCount.",
    "inputSchema": {
     "type": "object",
     "required": [
      "model",
      "contents"
     ],
     "properties": {
      "model": {
       "type": "string"
      },
      "contents": {
       "type": "array"
      },
      "generationConfig": {
       "type": "object"
      },
      "systemInstruction": {
       "type": "object"
      }
     }
    }
   }
  }
 }
}`
