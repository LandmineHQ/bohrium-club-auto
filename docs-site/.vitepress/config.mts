import { defineConfig } from 'vitepress'

// GitHub Pages project site: https://landminehq.github.io/bohrium-club-auto/
// Override with DOCS_BASE=/ when deploying at a custom domain.
// GitHub Actions automatically injects DOCS_BASE="/<repo-name>/" in deploy-docs.yml.
const base = (process.env.DOCS_BASE || '/bohrium-club-auto/').replace(/\/?$/, '/')

export default defineConfig({
  lang: 'zh-CN',
  title: 'Bohrium Club Auto',
  titleTemplate: ':title · Bohrium Club Auto',
  description: '玻尔用户社区每日任务自动化与积分容量控制台 —— Go 单文件二进制、Chrome 指纹、远程微信扫码、870 科研题库与 100 篇 arXiv 文献池。',
  base,
  cleanUrls: true,
  lastUpdated: true,
  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: `${base}logo.svg` }],
    ['meta', { name: 'theme-color', content: '#3b82f6' }],
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:title', content: 'Bohrium Club Auto' }],
    ['meta', { property: 'og:description', content: '玻尔用户社区每日任务自动化程序与积分容量监控控制台。' }]
  ],
  markdown: {
    theme: { light: 'github-light', dark: 'github-dark' },
    lineNumbers: true
  },
  themeConfig: {
    logo: '/logo.svg',
    siteTitle: 'Bohrium Club Auto',
    nav: [
      { text: '首页', link: '/' },
      { text: '使用指南', link: '/guide/introduction', activeMatch: '^/guide/' },
      { text: '管理 API', link: '/api/overview', activeMatch: '^/api/' },
      { text: '参考文档', link: '/reference/configuration', activeMatch: '^/reference/' },
      { text: 'GitHub', link: 'https://github.com/LandmineHQ/bohrium-club-auto' }
    ],
    sidebar: {
      '/guide/': [
        {
          text: '快速上手',
          items: [
            { text: '项目介绍', link: '/guide/introduction' },
            { text: '快速开始', link: '/guide/quickstart' },
            { text: '安装与部署', link: '/guide/installation' }
          ]
        },
        {
          text: '核心功能',
          items: [
            { text: '服务器端远程登录', link: '/guide/remote-login' },
            { text: '每日任务与立即终止', link: '/guide/daily-tasks' },
            { text: 'Web 管理控制台', link: '/guide/admin-console' },
            { text: '常见问题', link: '/guide/faq' }
          ]
        }
      ],
      '/api/': [
        {
          text: '管理 API 文档',
          items: [
            { text: 'API 概览与鉴权', link: '/api/overview' },
            { text: '完整端点参考', link: '/api/admin' }
          ]
        }
      ],
      '/reference/': [
        {
          text: '技术参考',
          items: [
            { text: '配置与环境变量', link: '/reference/configuration' },
            { text: '架构与上游契约', link: '/reference/architecture' },
            { text: '前端 Recon 快照与比对', link: '/reference/recon' },
            { text: '二进制与 Docker 发布', link: '/reference/binary-builds' },
            { text: '文档站本地预览与部署', link: '/reference/docs-site' }
          ]
        }
      ]
    },
    search: {
      provider: 'local',
      options: {
        translations: {
          button: { buttonText: '搜索文档', buttonAriaLabel: '搜索文档' },
          modal: {
            noResultsText: '没有找到匹配结果',
            resetButtonTitle: '清除搜索',
            footer: {
              selectText: '选择',
              navigateText: '切换',
              closeText: '关闭'
            }
          }
        }
      }
    },
    outline: { level: [2, 3], label: '本页目录' },
    docFooter: { prev: '上一页', next: '下一页' },
    darkModeSwitchLabel: '主题',
    sidebarMenuLabel: '菜单',
    returnToTopLabel: '回到顶部',
    lastUpdatedText: '最后更新',
    footer: {
      message: '自托管私有部署项目 · Go 静态单文件二进制 + 内置管理面板',
      copyright: 'MIT Licensed · Bohrium Club Auto'
    },
    socialLinks: [{ icon: 'github', link: 'https://github.com/LandmineHQ/bohrium-club-auto' }],
    editLink: {
      pattern: 'https://github.com/LandmineHQ/bohrium-club-auto/edit/main/docs-site/:path',
      text: '在 GitHub 上编辑此页'
    }
  }
})
