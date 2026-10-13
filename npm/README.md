# wxapkg-killer-mcp

npm wrapper for [wxapkg-killer](https://github.com/EchoTide/wxapkg-killer): an MCP server that unpacks WeChat mini-program and mini-game `.wxapkg` packages from the local cache, so an AI agent can read and analyze the code.

Analyze only apps you own or are authorized to test.

## Quick start

```bash
npx wxapkg-killer-mcp setup
```

Installing downloads the prebuilt binary for your platform from the GitHub release. `setup` registers it with the agents found on your machine (for example Claude Code, Cursor, Codex CLI, and OpenCode).

Check the result with `npx wxapkg-killer-mcp doctor`.

Documentation: https://github.com/EchoTide/wxapkg-killer

## License

MIT. The upstream `github.com/wux1an/wxapkg` dependency does not declare a license. See [NOTICE](https://github.com/EchoTide/wxapkg-killer/blob/main/NOTICE).
