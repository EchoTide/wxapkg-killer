# Contributing

GitHub 仓库名是 **wxapkg-killer**。发布时把本目录作为仓库根，不要把仓库命名为 `mcp`，也不要连同上一级目录一起推送。上一级目录里有解包产物，不属于本项目。

```bash
go test ./...
go build -trimpath -ldflags "-s -w -X main.version=dev" -o wxapkg-killer.exe .
```

不要提交 `wxapkg-killer.exe`、`*.exe~` 或 `out/`。

改配置合并逻辑时，跑 `go test` 里的 JSON / TOML / OpenCode 用例。OpenCode 的写入必须保留无关字段，不能整文件重排。
