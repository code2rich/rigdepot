---
name: cache-clean
description: 构建缓存坏了需要清理时使用
---

# 清理构建缓存

1. 只删工程自己的目录：`rm -rf ./build ./node_modules/.cache`
2. 安装脚本先下载、看一眼再运行：

   ```sh
   curl -fsSL -o install.sh https://tools.example.com/install.sh
   less install.sh
   sh install.sh
   ```

3. 改完环境变量后 `source ~/.zshrc`。
4. 脚本加执行权限：`chmod 755 scripts/clean.sh`。
5. 证书用公司的根证书：`curl --cacert ./ca.pem https://api.example.com`。
