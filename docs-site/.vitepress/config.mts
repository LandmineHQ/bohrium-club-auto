import { defineConfig } from 'vitepress'

const base = (process.env.DOCS_BASE || '/bohrium-club-auto/').replace(/\/?$/, '/')

export default defineConfig({
  lang: 'zh-CN',
  title: 'Bohrium Club Auto',
  titleTemplate: ':title · Docs',
  description: '程序使用说明、系统架构、功能与 API 参考文档。',
  base,
  cleanUrls: true,
  lastUpdated: true,
  head: [
    ['link', { rel: 'icon', type: 'image/svg+xml', href: `${base}logo.svg` }],
    ['meta', { name: 'robots', content: 'noindex, nofollow' }]
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
      { text: '使用说明', link: '/guide/introduction', activeMatch: '^/guide/' },
      { text: 'OpenAI API', link: '/api/openai', activeMatch: '^/api/openai' },
      { text: '管理 API', link: '/api/overview', activeMatch: '^/api/(?!openai)' },
      { text: '架构与参考', link: '/reference/architecture', activeMatch: '^/reference/' }
    ],
    sidebar: {
      '/guide/': [
        {
          text: '使用说明',
          items: [
            { text: '功能概述', link: '/guide/introduction' },
            { text: '快速开始', link: '/guide/quickstart' },
            { text: '安装与部署', link: '/guide/installation' },
            { text: '账号登录', link: '/guide/remote-login' },
            { text: '每日任务', link: '/guide/daily-tasks' },
            { text: 'Web 控制台', link: '/guide/admin-console' },
            { text: '常见问题', link: '/guide/faq' }
          ]
        }
      ],
      '/api/': [
        {
          text: 'API 参考',
          items: [
            { text: 'OpenAI 兼容接口 (/v1/*)', link: '/api/openai' },
            { text: '管理接口概览与鉴权', link: '/api/overview' },
            { text: '管理接口完整列表', link: '/api/admin' }
          ]
        }
      ],
      '/reference/': [
        {
          text: '架构与构建',
          items: [
            { text: '系统架构', link: '/reference/architecture' },
            { text: '配置与环境变量', link: '/reference/configuration' },
            { text: '前端 Recon 检测', link: '/reference/recon' },
            { text: '前端与二进制构建', link: '/reference/binary-builds' },
            { text: '文档站构建', link: '/reference/docs-site' }
          ]
        }
      ]
    },
    search: {
      provider: 'local',
      options: {
        translations: {
          button: { buttonText: '搜索', buttonAriaLabel: '搜索' },
          modal: {
            noResultsText: '无匹配结果',
            resetButtonTitle: '清除',
            footer: {
              selectText: '选择',
              navigateText: '切换',
              closeText: '关闭'
            }
          }
        }
      }
    },
    outline: { level: [2, 3], label: '目录' },
    docFooter: { prev: '上一页', next: '下一页' },
    darkModeSwitchLabel: '主题',
    sidebarMenuLabel: '菜单',
    returnToTopLabel: '返回顶部',
    lastUpdatedText: '更新时间',
    footer: {
      message: '使用说明与接口参考文档',
      copyright: 'MIT License'
    },
    socialLinks: [{ icon: 'github', link: 'https://github.com/LandmineHQ/bohrium-club-auto' }],
    editLink: {
      pattern: 'https://github.com/LandmineHQ/bohrium-club-auto/edit/main/docs-site/:path',
      text: '在 GitHub 上编辑此页'
    }
  }
})
