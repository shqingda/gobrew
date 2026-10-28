# Apply 阶段归因：2026-10-28

本页记录正式测量组。开始前已协调暂停其他代理的本机测试和构建。先前可能与短时间测试重叠的探索组完整保留在 [exploration-interference](exploration-interference/README.md)，未丢弃、混入或挑选其中的样本。

本实验使用与[同日五轮基准](../../benchmarks/2026-10-28-m3-pro-046d271.json)相同的 gobrew 二进制：源码提交 `046d271dfbb1f2aedbcf312551978ffb7ce91092`，SHA-256 `afa1f2aafd5806ea8af4382b76e30a7fe8bc33a0a9bc5f97c744fe3b27fe882f`。环境为 Apple Silicon、macOS 27.0.1。完整原始数据和命令退出码见 [report.json](report.json)，两组每条命令的 stdout/stderr 按报告文件名保存在 [raw-output.tar.gz](raw-output.tar.gz)。

## 方法与结果

在新建的短 prefix、HOME、XDG 配置、缓存和临时目录内，从官方签名目录安装 `ca-certificates 2026-10-20`，未使用 Homebrew 或现有安装。该次完整安装的 Apply 为 **7433.961 ms**。

随后直接执行 bottle 中未经修改的 `libexec/post-install` 五次，传入原始 Mozilla PEM 和与正常安装相同的输出路径；使用与 gobrew post-install runner 相同的 PATH、HOME、USER、LOGNAME、LANG、TMPDIR 及工作目录。每次运行前只删除派生的 `cert.pem`，运行后验证输出非空，且 SHA 与完整安装生成的文件相同。系统 keychain 只读，未修改；安装目录在实验结束后删除。

已检查原脚本：每次创建独立临时目录，读取两处系统 keychain，对证书逐一调用 `openssl`、`security verify-cert` 完成有效性及信任检查，再合并、去重 Mozilla 证书，最后原子替换输出。没有“目标已存在则返回”的缓存快路径，本实验也未跳过这些步骤。原脚本及输入的 SHA、bottle 摘要在报告中，可从相同官方 bottle 复核；不在仓库另行复制上游脚本。

| 轮次 | 初始化脚本耗时（ms） | 输出证书数 | 输出字节数 |
| --- | ---: | ---: | ---: |
| 1 | 6240.875 | 189 | 286958 |
| 2 | 6278.631 | 189 | 286958 |
| 3 | 6482.646 | 189 | 286958 |
| 4 | 6709.349 | 189 | 286958 |
| 5 | 6590.162 | 189 | 286958 |

中位数为 **6482.646 ms**。五次输出 SHA 均为 `56906320836d6e7a4b91d8c86f16156c81051ca81419f886c9fc3c8868e7fb5b`，与首次完整安装一致。所有命令退出 0，doctor、卸载和清理通过，临时目录已删除。

## 结论与限制

同日主基准热重装的 Apply 中位数为 **7361.451 ms**，每轮占总耗时 99.80%–99.86%。本实验的完整证书初始化耗时与其接近，支持将初始化视为该集合的主要耗时来源；该阶段承担真实的证书信任与合并工作。当前没有已证明值得保留的最小产品优化，因此不修改上游脚本、不省略检查，也不为毫秒级 Plan/热 Prepare 增加缓存或新结构。

两组测量并非同一轮嵌套采样：本实验只安装证书包，主基准包含四个包；初次安装与重复脚本调用可能受系统缓存影响，两个中位数不能直接相减得到 gobrew 自身的精确开销。约 6.483 / 7.361 的比例仅帮助判断量级，不代表严格归因百分比。本实验不构成优化前后对照，也不证明所有 Apply 开销都不可优化。

## 复现

先在仓库根目录构建未托管的 dev 二进制。将实验脚本复制到新的目录再运行，以保留原记录：

```sh
CGO_ENABLED=0 go build -trimpath -o bin/gobrew ./cmd/gobrew
evidence=$(mktemp -d /tmp/gobrew-apply-evidence.XXXXXX)
cp docs/verification/2026-10-28-apply-profile/profile.py "$evidence/"
/usr/bin/python3 "$evidence/profile.py" "$PWD/bin/gobrew"
```

脚本要求当前目录仍提供相同版本 fixture；若上游版本改变则明确失败，不能把不同版本混入本记录。仅依赖 Python 标准库和 macOS 系统工具。
