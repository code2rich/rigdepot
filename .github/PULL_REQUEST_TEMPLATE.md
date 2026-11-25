<!--
English and Chinese are both fine. 中文英文都可以。
One pull request, one goal. 一个 PR 一个目标。
Guide / 指南: CONTRIBUTING.md · CONTRIBUTING.zh-CN.md
-->

## What and why / 做了什么，为什么

<!-- What changes for a user or a contributor, and the reason. Link the issue: "Fixes #123".
     对使用者或贡献者来说什么变了，原因是什么。关联 issue：「Fixes #123」。 -->

## What I ran / 跑了什么

<!-- Paste the result table printed by `go run ./tools/dev check`. Steps that did not run stay in the table as "not run".
     粘贴 `go run ./tools/dev check` 结尾打印的结果表。没跑的步骤留在表里，写「not run」。 -->

```text

```

Tests were really executed on / 测试真正执行过的平台:

- [ ] macOS
- [ ] Linux (`go run ./tools/dev check-linux` counts / 也算)
- [ ] Windows

## Acceptance checklist / 验收清单

- [ ] **A test fails without this change.** I put the old code back, ran the new test and saw a real assertion failure. Which test, and what it printed:
      **有一个去掉改动就失败的测试。** 我把旧代码放回去跑了新测试，看到了真正的断言失败。是哪个测试、输出了什么：
- [ ] **Same kind, same fix.** I searched for the same pattern elsewhere (every adapter, every command, every caller) and fixed those here too, or there are none.
      **同类都修了。** 我搜过别处有没有同样的模式（每个适配器、每个命令、每个调用方），一并修了，或者确实没有。
- [ ] **Real use.** For a change to a tool's adapter: verified against the real tool in a temporary home directory (tool and version: ___ ), or I state here that it was **not verified against the real tool**.
      **真实使用。** 改了某个工具的适配器：在临时主目录里对着真实的工具验证过（工具和版本：___ ），或者在这里写明**没有在真实工具上验证**。
- [ ] **Documentation is in sync**, or none is needed because:
      **文档同步了**，或者不需要，因为：
- [ ] **Every commit is signed off** (`git commit -s`), which means I agree to the [DCO](../blob/main/DCO).
      **每个提交都带 `Signed-off-by`**（`git commit -s`），表示我同意 [DCO](../blob/main/DCO)。
- [ ] New source files start with the license header; nothing here contains secrets, tokens, internal addresses or real people's names.
      新的源文件带协议头；改动里没有密钥、令牌、内部地址和真实人名。

## Needs a maintainer's eyes / 需要维护者亲自看

<!-- Tick what applies. These are not forbidden; the review pauses until a maintainer has looked.
     有就勾上。这些不是不行，而是评审会暂停，等维护者亲自看。 -->

- [ ] Adds a third-party dependency / 新增第三方依赖
- [ ] Adds a network address, reporting, or sends data somewhere new / 新增网络地址、上报，或把数据发到新的地方
- [ ] Reads or writes credential files or the keychain, or makes an adapter write to a new location / 读写凭据文件、钥匙串，或让适配器写到新的位置
- [ ] Changes `.github/`, the release configuration or the install scripts / 改了 `.github/`、发布配置或安装脚本
- [ ] Relaxes a check rule or collects more fields / 放宽检查规则，或扩大采集的字段
