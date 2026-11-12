// Browser entry in the ModuleLoader format supported by DSH 0.2.2.
window.__ModuleLoader__.load({
  id: 'dsh-plugin-talus',
  factory(require) {
    const React = require('react');
    const h = React.createElement;
    const en = {
      url: 'Talus URL',
      urlHint: 'Base address without /api/v1. Leave blank to use TALUS_URL, or http://localhost:8080 when it is unset.',
      key: 'API key',
      keyHint: 'Saved keys are never filled back in. Leave blank to keep the saved key. If none is saved, TALUS_API_KEY is used.',
      save: 'Save', saving: 'Saving…', saved: 'Configuration saved. New requests use it immediately.',
      clear: 'Clear saved API key', clearPending: 'The saved API key will be cleared when you save.',
      reload: 'Discard edits', loading: 'Loading configuration…',
      unavailable: 'Enable the Talus plugin to configure it.', readOnly: 'This profile does not allow configuration changes.',
      failed: 'Could not save. Your edits are preserved. Discard edits to load the current configuration, then try again.',
      invalidUrl: 'Enter an HTTP or HTTPS base URL without credentials, a query, a fragment, or /api/v1.',
      invalidKey: 'The API key must not contain line breaks.',
      storage: 'Saved in this DSH profile’s private local configuration. The key is masked in the UI and redacted from settings reads.',
    };
    const zh = {
      url: 'Talus 地址',
      urlHint: '实例基础地址，不含 /api/v1。留空使用 TALUS_URL；该变量未设置时使用 http://localhost:8080。',
      key: 'API key',
      keyHint: '已保存的密钥不会回填。留空保留已保存的密钥；未保存时使用 TALUS_API_KEY。',
      save: '保存', saving: '保存中…', saved: '配置已保存，后续请求立即生效。',
      clear: '清除已保存的 API key', clearPending: '点击保存后将清除已保存的 API key。',
      reload: '放弃修改', loading: '正在加载配置…',
      unavailable: '请先启用 Talus 插件再进行配置。', readOnly: '当前 profile 不允许修改配置。',
      failed: '保存失败，已保留输入。可放弃修改以读取当前配置，再重新填写并保存。',
      invalidUrl: '请填写 HTTP 或 HTTPS 基础地址，不包含账号密码、查询参数、片段或 /api/v1。',
      invalidKey: 'API key 不能包含换行。',
      storage: '配置保存在当前 DSH profile 的私有本地配置文件中。密钥输入会遮罩，设置读取接口会脱敏。',
    };
    const fieldStyle = {
      width: '100%', padding: '9px 12px', borderRadius: 8, boxSizing: 'border-box',
      border: '1px solid color-mix(in srgb, currentColor 25%, transparent)',
      background: 'transparent', color: 'inherit', font: 'inherit',
    };
    const buttonStyle = {
      padding: '8px 14px', borderRadius: 8, cursor: 'pointer', font: 'inherit',
      background: 'transparent', color: 'inherit',
      border: '1px solid color-mix(in srgb, currentColor 25%, transparent)',
    };

    function TalusPanel({ talusForm, t }) {
      const subscribe = React.useCallback(listener => talusForm.subscribe(listener), [talusForm]);
      const getSnapshot = React.useCallback(() => talusForm.getSnapshot(), [talusForm]);
      const snapshot = React.useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
      const [url, setUrl] = React.useState('');
      const [apiKey, setApiKey] = React.useState('');
      const [clearKey, setClearKey] = React.useState(false);
      const [dirty, setDirty] = React.useState(false);
      const [revision, setRevision] = React.useState();
      const [saving, setSaving] = React.useState(false);
      const [message, setMessage] = React.useState('');
      const alive = React.useRef(true);
      React.useEffect(() => {
        alive.current = true;
        return () => { alive.current = false; };
      }, []);
      React.useEffect(() => {
        if (!dirty) {
          setUrl(typeof snapshot.value?.url === 'string' ? snapshot.value.url : '');
          setRevision(snapshot.revision);
        }
      }, [snapshot, dirty]);
      const editable = snapshot.status === 'ready' && snapshot.writable && !saving;
      function edit() {
        if (!dirty) setRevision(snapshot.revision);
        setDirty(true);
        setMessage('');
      }
      function reset() {
        setUrl(typeof snapshot.value?.url === 'string' ? snapshot.value.url : '');
        setApiKey(''); setClearKey(false); setDirty(false); setMessage('');
        setRevision(snapshot.revision);
      }
      async function save(event) {
        event.preventDefault();
        if (!editable || !dirty) return;
        const address = url.trim().replace(/\/+$/, '');
        if (address) {
          try {
            const parsed = new URL(address);
            if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password ||
              parsed.search || parsed.hash || /\/api\/v1$/i.test(parsed.pathname)) throw new Error('invalid');
          } catch { setMessage('invalidUrl'); return; }
        }
        if (/[\r\n]/.test(apiKey)) { setMessage('invalidKey'); return; }
        const ops = [{ op: 'set', path: ['url'], value: address }];
        if (apiKey.trim()) ops.push({ op: 'set', path: ['apiKey'], value: apiKey.trim() });
        else if (clearKey) ops.push({ op: 'set', path: ['apiKey'], value: '' });
        setSaving(true); setMessage('');
        try {
          const accepted = await talusForm.mutate(ops, revision);
          if (!alive.current) return;
          if (accepted) {
            setApiKey(''); setClearKey(false); setDirty(false); setMessage('saved');
          } else setMessage('failed');
        } catch {
          if (alive.current) setMessage('failed');
        } finally {
          if (alive.current) setSaving(false);
        }
      }
      if (snapshot.status === 'loading') return h('p', null, t('loading'));
      if (snapshot.status !== 'ready') return h('p', { role: 'status' }, t('unavailable'));
      return h('form', { onSubmit: save, style: { maxWidth: 640, display: 'grid', gap: 16, padding: '16px 0' } },
        h('label', { style: { display: 'grid', gap: 8 } },
          h('span', { style: { fontWeight: 600 } }, t('url')),
          h('input', {
            type: 'url', name: 'talus-url', value: url, placeholder: 'http://localhost:8080',
            disabled: !editable, autoComplete: 'url', spellCheck: false, style: fieldStyle,
            onChange: event => { edit(); setUrl(event.target.value); },
          }),
          h('small', { style: { opacity: 0.7 } }, t('urlHint'))),
        h('label', { style: { display: 'grid', gap: 8 } },
          h('span', { style: { fontWeight: 600 } }, t('key')),
          h('input', {
            type: 'password', name: 'talus-api-key', value: apiKey, autoComplete: 'new-password',
            disabled: !editable, spellCheck: false, style: fieldStyle,
            onChange: event => { edit(); setApiKey(event.target.value); setClearKey(false); },
          }),
          h('small', { style: { opacity: 0.7 } }, t('keyHint'))),
        clearKey ? h('p', { role: 'status' }, t('clearPending')) : null,
        message ? h('p', { role: message === 'saved' ? 'status' : 'alert' }, t(message)) : null,
        !snapshot.writable ? h('p', { role: 'status' }, t('readOnly')) : null,
        h('div', { style: { display: 'flex', flexWrap: 'wrap', gap: 8 } },
          h('button', {
            type: 'submit', disabled: !editable || !dirty,
            style: { ...buttonStyle, background: '#2563eb', color: '#fff', opacity: editable && dirty ? 1 : 0.5 },
          }, t(saving ? 'saving' : 'save')),
          h('button', {
            type: 'button', disabled: !editable,
            style: buttonStyle, onClick: () => { edit(); setApiKey(''); setClearKey(true); },
          }, t('clear')),
          h('button', { type: 'button', disabled: !dirty || saving, onClick: reset, style: buttonStyle }, t('reload'))),
        h('small', { style: { opacity: 0.65 } }, t('storage')));
    }

    return {
      name: 'talus-ui',
      inject: ['slots', 'configForms', 'locale'],
      apply(ctx) {
        ctx.effect(() => ctx.locale.register('talus', { en, zh }));
        ctx.slots.inject('plugins.bundle.config', () => ctx.slots.register({
          name: 'plugins.bundle.config', key: 'dsh-plugin-talus', locale: 'talus',
          inject: () => ({ talusForm: ctx.configForms.get('talus') }),
        }, TalusPanel));
      },
    };
  },
});
