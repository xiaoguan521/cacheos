// ESLint flat config（package.json 为 "type": "module"，因此使用 .js）。
// 规则集以各插件的 recommended 为基础，覆盖 src 与 src-portal 下的 .ts 与 .vue。
import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import pluginVue from 'eslint-plugin-vue'

export default tseslint.config(
  {
    ignores: ['dist/**', 'portal/dist/**', 'node_modules/**']
  },
  js.configs.recommended,
  tseslint.configs.recommended,
  pluginVue.configs['flat/recommended'],
  {
    rules: {
      // 142 处显式 any 属于类型严格性问题，不影响运行；逐个替换会大面积改动并可能引出类型错误，降为 warn 保留信号。
      '@typescript-eslint/no-explicit-any': 'warn',
      // 解构剩余项用于“剔除某个字段”（如 const { customer_id: _c, ...body } = draft），
      // 被剔除的变量是有意未使用的，因此允许 rest sibling。
      '@typescript-eslint/no-unused-vars': ['error', { ignoreRestSiblings: true }],
      // 页面组件名如 Login、Audit 是单词文件名，改名会牵动路由与导入，属于命名风格，降为 warn。
      'vue/multi-word-component-names': 'warn',
      // 以下为纯排版规则，全仓库会产生约 1800 条警告且只涉及换行与缩进，关闭以免淹没真正的问题。
      'vue/max-attributes-per-line': 'off',
      'vue/singleline-html-element-content-newline': 'off',
      'vue/html-indent': 'off',
      'vue/first-attribute-linebreak': 'off',
      'vue/html-closing-bracket-newline': 'off',
      'vue/html-self-closing': 'off'
    }
  },
  {
    files: ['**/*.vue'],
    languageOptions: {
      parserOptions: {
        parser: tseslint.parser,
        extraFileExtensions: ['.vue']
      }
    },
    rules: {
      // .vue 中的浏览器全局（window、document、URL 等）由 vue-tsc 基于 DOM lib 检查；
      // typescript-eslint 对 TS 文件同样关闭 no-undef，这里与之保持一致，避免重复且误报的规则。
      'no-undef': 'off'
    }
  }
)
