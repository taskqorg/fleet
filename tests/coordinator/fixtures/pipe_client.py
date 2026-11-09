"""Test stand-in for Pipe's read-lane client (the module pipe_search.py loads from connection.clientPath).

Same contract as the real client: answer(message, config, runtime) for tools/call posts the arguments to
{url}/v1/read/{tool} with the bearer token and X-Pipe-Runtime (and X-Pipe-Task from PIPE_TASK), and returns an MCP
tool result whose text is the response body, or "Pipe <tool> failed: HTTP <code>: <body>" on an HTTP error.
"""
import json
import os
import urllib.error
import urllib.request


def access_headers(path):
    return {}


def answer(message, config, runtime):
    parameters = message.get("params") or {}
    tool = parameters.get("name")
    headers = {"Authorization": f"Bearer {config['token']}", "Content-Type": "application/json", "X-Pipe-Runtime": runtime}
    if os.environ.get("PIPE_TASK"):
        headers["X-Pipe-Task"] = os.environ["PIPE_TASK"]
    request = urllib.request.Request(f"{config['url']}/v1/read/{tool}", json.dumps(parameters.get("arguments") or {}).encode(), headers)
    try:
        with urllib.request.urlopen(request, timeout=30) as reply:
            return {"isError": False, "content": [{"type": "text", "text": reply.read().decode()}]}
    except urllib.error.HTTPError as error:
        return {"isError": True, "content": [{"type": "text", "text": f"Pipe {tool} failed: HTTP {error.code}: {error.read(4096).decode(errors='replace')}"}]}
    except (OSError, ValueError) as error:
        return {"isError": True, "content": [{"type": "text", "text": f"Pipe {tool} failed: Pipe is unreachable: {error}"}]}
