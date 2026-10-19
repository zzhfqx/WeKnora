# Git 分支创建与推送完整指南

> 适用场景：基于上游开源仓库（如 Tencent/WeKnora）进行二次开发，使用自己的 fork 仓库推送代码。

---

## 一、前期准备（只需做一次）

### 1. Fork 上游仓库

在浏览器打开上游仓库 → 右上角 **Fork** → 选自己的账号。

### 2. 配置 SSH 密钥（推荐，比 HTTPS 稳定）

SSH 密钥只需配置一次，所有仓库通用。

**检查是否已有密钥：**
```bash
ls ~/.ssh/id_rsa.pub
```

**如果没有，生成一个：**
```bash
ssh-keygen -t ed25519 -C "your_email@example.com"
# 一路回车即可
```

**添加到 GitHub：**
```bash
cat ~/.ssh/id_ed25519.pub   # 或 id_rsa.pub
```
复制输出内容 → 打开 [github.com/settings/keys](https://github.com/settings/keys) → **New SSH key** → 粘贴保存。

**测试连接：**
```bash
ssh -T git@github.com
# 看到 Hi 你的用户名! ... 就说明成功了
```

### 3. 配置远程源

先进入项目目录，查看当前远程状态：
```bash
cd /path/to/your/project
git remote -v
```

根据你最初 clone 的来源不同，有两种配置思路，**最终结果一样**：

#### 思路 A：clone 的是自己的 fork（推荐）
`origin` 已经指向你的 fork，只需添加上游仓库，然后把 origin 改成 SSH。

```bash
# 1. 添加上游官方仓库
git remote add upstream https://github.com/上游用户名/仓库名.git

# 2. 把 origin 改成 SSH 协议（更稳定，避免 443 端口超时）
git remote set-url origin git@github.com:你的用户名/仓库名.git
```

#### 思路 B：clone 的是上游官方仓库
`origin` 当前指向官方仓库，需要先改名腾出 origin，再加自己的 fork。

```bash
# 1. 把原来的 origin 改名为 upstream（上游原仓库）
git remote rename origin upstream

# 2. 添加自己的 fork 为 origin（SSH 协议，推荐）
git remote add origin git@github.com:你的用户名/仓库名.git
```

#### 备选：使用 HTTPS + Token 方式（不推荐，容易超时）
如果 SSH 不可用，可以用 HTTPS + Personal Access Token：

```bash
# 修改已存在的 origin（origin 已存在时用这个）
git remote set-url origin https://你的用户名:你的TOKEN@github.com/你的用户名/仓库名.git

# 新增一个远程仓库（origin 不存在时用这个）
git remote add origin https://你的用户名:你的TOKEN@github.com/你的用户名/仓库名.git
```

> **Token 获取方式**：打开 [github.com/settings/tokens](https://github.com/settings/tokens) → Generate new token (classic) → 勾选 `repo` 权限 → 生成后复制保存（只显示一次）。

#### 确认配置结果

```bash
git remote -v
# origin    git@github.com:你的用户名/仓库名.git (fetch/push)   ← 你的 fork，用来推送
# upstream  https://github.com/上游用户名/仓库名.git (fetch/push) ← 官方仓库，用来拉取更新
```

---

## 二、日常开发流程（每次新功能都走一遍）

### 第 1 步：拉取上游最新代码

```bash
git checkout main
git pull upstream main
```

### 第 2 步：创建并切换到新分支

```bash
git checkout -b 分支名
```

**分支命名建议：**
- `feat/xxx` — 新功能
- `fix/xxx` — 修复 bug
- `dev/xxx` — 开发调试分支

### 第 3 步：写代码

（略）

### 第 4 步：查看修改了什么

```bash
git status    # 看哪些文件变了
git diff      # 看具体改动
```

### 第 5 步：提交修改

```bash
git add .
git commit -m "描述你做了什么"
```

### 第 6 步：推送到自己的 GitHub

| 场景 | 命令 |
|------|------|
| 第一次推送新分支（推荐，绑定上游） | `git push -u origin 分支名` |
| 第一次推送新分支（不设关联） | `git push origin 分支名` |
| 已经设置过上游，以后推送 | `git push` 或 `git push origin` |

> **-u 参数说明**：`-u` 是 `--set-upstream` 的缩写，绑定后下次直接 `git push` 就行，不用再写分支名。

---

## 三、常用操作速查

| 需求 | 命令 |
|------|------|
| 查看当前分支 | `git branch` |
| 查看所有分支（含远程） | `git branch -a` |
| 查看分支跟踪关系 | `git branch -vv` |
| 查看提交历史 | `git log --oneline` |
| 切换分支 | `git checkout 分支名` |
| 拉取远程更新 | `git fetch origin` |
| 拉取并合并当前分支 | `git pull` |
| 同步上游 main 到本地 main | `git checkout main && git pull upstream main` |
| 撤销工作区修改 | `git checkout -- 文件名` |
| 撤销最近一次提交（保留改动） | `git reset --soft HEAD~1` |
| 删除本地分支 | `git branch -d 分支名` |
| 删除远程分支 | `git push origin --delete 分支名` |

---

## 四、常见问题

| 问题 | 原因 | 解决 |
|------|------|------|
| `Failed to connect to github.com port 443: 连接超时` | HTTPS 端口被网络干扰 | 改用 SSH 协议（见 1.2 节） |
| `Permission denied (publickey)` | SSH key 没加到 GitHub | 重新添加公钥到 github.com/settings/keys |
| `remote: Support for password authentication was removed` | Token 失效或用了密码 | 用 SSH，或重新生成 Token |
| `fatal: remote origin already exists` | origin 已存在 | 用 `git remote set-url origin ...` 而不是 `add` |
| 推送时报分支已存在 | 远程已有同名分支 | 换个分支名，或 `git checkout 已有分支名` 切换过去 |
| 想拉取上游仓库更新 | — | `git pull upstream main` |

---

## 五、你当前项目的状态参考

以 WeKnora 为例，配置完成后应该是这样的：

```
本地两个工作目录
├── fqx_weknora/WeKnora/   调试用，当前分支 pg-fqx
└── my_weknora/WeKnora/    正式版，当前分支 pg-zzh

两个目录的远程配置相同：
  origin    → git@github.com:zzhfqx/WeKnora.git  （你的 fork）
  upstream  → https://github.com/Tencent/WeKnora.git  （上游官方）
```
