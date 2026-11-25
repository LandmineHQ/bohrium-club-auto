---
layout: home

hero:
  name: Bohrium Club Auto
  text: 玻尔社区每日任务自动化与积分控制台
  tagline: 纯 Go 零依赖静态单文件二进制、Chrome 浏览器出站指纹、服务器远程微信扫码、870 科研题库 + 100 篇 arXiv 文献随机上传提问后立即终止，以及完整的管理员 Web 仪表盘。
  image:
    src: /logo.svg
    alt: Bohrium Club Auto
  actions:
    - theme: brand
      text: 快速开始
      link: /guide/quickstart
    - theme: alt
      text: 浏览管理 API
      link: /api/overview
    - theme: alt
      text: 二进制与 Docker 发布
      link: /reference/binary-builds

features:
  - icon: 📱
    title: 服务器端远程微信扫码登录
    details: 服务端直接向 platform.bohrium.com 申请官方微信二维码并在 Web 面板扫码登录，无需本地挂机，无图形验证码，后台自动续期 brmToken。
  - icon: 📊
    title: 三大类剩余积分与容量监控
    details: 一站式查看个人/组织光子余额、钱包余额、7 日即将过期批次与收支流水、会员功能剩余配额（文献/对话/深度研究）及知识库存储容量。
  - icon: ⚡
    title: 一键领取与 3 项 Web 任务自动执行
    details: 自动跳过客户端任务（第 2 项）与已废弃的旧版 200 光子签到接口；支持一键领取、一键执行、单项手动触发及每日定时调度。
  - icon: 📚
    title: 870 科研题库 + 100 篇真实文献 PDF
    details: 二进制内嵌 330 条科学空间问题、320 条学术写作问题、220 条文献阅读问题及 100 篇 arXiv 开放获取论文目录，提问并上报后立即 cancel 终止。
  - icon: 🌐
    title: Chrome 浏览器出站指纹
    details: 所有出站请求自动注入桌面端 Chrome User-Agent 与 Sec-CH-UA Client Hints，彻底屏蔽 Go 默认 HTTP 客户端标识，支持自定义代理与 UA。
  - icon: 🤖
    title: OpenAI 兼容网关（Chat & Responses）
    details: 完整还原 Bohrium 客户端（SciX）原生 Sigma LLM 上游协议与 x-scix-execution 交互，提供 /v1/models、/v1/chat/completions 与 /v1/responses 全能力及双向跨协议桥接。
  - icon: 🔍
    title: 前端 Recon 漂移监控与全套 CI/CD
    details: 内置 recon pull / recon diff 检查线上前端源码与关键 API 契约变化；配套多平台静态二进制 Release、多架构 GHCR Docker 与文档站发布流水线。
---
