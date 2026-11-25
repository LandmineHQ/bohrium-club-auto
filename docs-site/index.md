---
layout: home

hero:
  name: Bohrium Club Auto
  text: 使用说明、架构与 API 文档
  tagline: 多账号托管、定时任务调度、Web 管理控制台与 OpenAI 兼容路由网关。
  actions:
    - theme: brand
      text: 快速开始
      link: /guide/quickstart
    - theme: alt
      text: 系统架构
      link: /reference/architecture
    - theme: alt
      text: API 参考
      link: /api/overview

features:
  - title: 多账号与远程登录
    details: 支持微信扫码、第三方 OAuth、验证码、密码与 Token 导入，数据持久化于本地 SQLite（data/bohrium.db），各账号独立 storeId 并自动维护 Token 续期。
  - title: 随机波动每日任务
    details: 覆盖全部 4 项每日任务，按账号分配每日错峰执行时间并保持账号间隔离，任务触发后及时终止流以控制消耗。
  - title: OpenAI 兼容路由池
    details: 新账号默认不入池，手动拖入后按可用余额比例加权分配新会话，绑定关系在 SQLite 中最长缓存 7 天，支持 /v1/models、/v1/balance、/v1/chat/completions 与 /v1/responses。
  - title: 模块化前端与单文件交付
    details: 控制台基于 React + Vite + TypeScript + shadcn/ui 构建并嵌入 Go 静态二进制，提供多账号管理、拖拽路由池、资产监控、日志与系统设置。
---
