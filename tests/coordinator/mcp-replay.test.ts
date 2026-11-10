// fleet-stack L4: preserve stdio MCP behavior and concurrent durable journal effects during the Python removal.
import {test} from 'node:test';
import {execFileSync} from 'node:child_process';
import {executorBinary} from './executor-binary.ts';
test('L4 captured MCP exchanges and journal effects match the Rust subcommands',()=>{
 execFileSync('python3',['tests/executor/mcp_replay.py',executorBinary()],{stdio:'inherit',timeout:60000});
});
