// Browser entry in the ModuleLoader format supported by DSH 0.2.2.
window.__ModuleLoader__.load({
  id: 'dsh-plugin-talus',
  factory(require) {
    const React = require('react');
    const h = React.createElement;
    const en = {
      url: 'Talus URL', urlHint: 'HTTPS base address without /api/v1. Each address has its own saved key.',
      key: 'API key', keyHint: 'Enter the address first, then its key. Saved keys are never filled back in. Leave blank to keep a key already saved for this address; editing the address clears any unsaved key.',
      save: 'Save', saving: 'Saving…', saved: 'Configuration saved. New requests use it immediately.',
      cleared: 'The saved key was cleared. Configure an HTTPS address and its key before making requests.',
      clear: 'Clear saved API key', clearPending: 'Clear selected. Saving will request removal of the key for the address above.',
      reload: 'Discard edits', loading: 'Loading configuration…',
      keyChecking: 'Checking the saved key for this address…',
      keyConfigured: 'A key is saved for this address.', keyMissing: 'No key is saved for this address.',
      keyUnavailable: 'Could not read credential status. Your inputs are preserved; try saving again.',
      keyReadOnly: 'The credential store does not allow changes to this key.',
      legacyKey: 'An old key remains in plugin settings. It is not used for requests; enter and save the key here to move to the native credential store.',
      unavailable: 'Enable the Talus plugin to configure it.', readOnly: 'This profile does not allow configuration changes.',
      failed: 'Could not finish saving. Your edits are preserved. Discard edits to load the current configuration, then try again.',
      failedKeyPresent: 'Could not finish saving. At the last check, a key was configured for the address above. Your edits are preserved; discard them to load the latest configuration.',
      failedKeyMissing: 'Could not finish saving. At the last check, no key was configured for the address above. Your edits are preserved; discard them to load the latest configuration.',
      failedKeyUnknown: 'Could not finish saving or check the key status. Your edits are preserved; discard them to load the latest configuration, then check the key before retrying.',
      missingKey: 'Enter an API key for this address before saving. A key for another address is not reused.',
      invalidUrl: 'Enter an HTTPS base URL without credentials, a query, a fragment, or /api/v1.',
      invalidKey: 'The API key must not contain line breaks.',
      secureContext: 'Open this DSH page over HTTPS or localhost to configure credentials.',
      legacyHttp: 'HTTP requests are disabled. Change this address to HTTPS; you can still clear a key saved for the old HTTP address.',
      storage: 'Keys use DSH’s native credential store, which defaults to a private local file. It is not an encrypted OS keychain and does not isolate processes running as the same OS user.',
    };
    const zh = {
      url: 'Talus 地址', urlHint: 'HTTPS 实例基础地址，不含 /api/v1。不同地址分别保存对应密钥。',
      key: 'API key', keyHint: '请先填写地址，再填写对应密钥。已保存的密钥不会回填，留空保留此地址已有的密钥；修改地址会清空尚未保存的密钥。',
      save: '保存', saving: '保存中…', saved: '配置已保存，后续请求立即生效。',
      cleared: '已清除保存的密钥。发起请求前，请配置 HTTPS 地址及对应密钥。',
      clear: '清除已保存的 API key', clearPending: '已选择清除；保存时将请求移除上方地址对应的密钥。',
      reload: '放弃修改', loading: '正在加载配置…',
      keyChecking: '正在检查此地址的密钥状态…',
      keyConfigured: '此地址已保存密钥。', keyMissing: '此地址尚未保存密钥。',
      keyUnavailable: '无法读取凭据状态，已保留输入；可再次尝试保存。',
      keyReadOnly: '当前凭据库不允许修改此密钥。',
      legacyKey: '旧插件设置中仍有密钥，旧值不会用于请求；请在此重新输入并保存，以转入原生凭据库。',
      unavailable: '请先启用 Talus 插件再进行配置。', readOnly: '当前 profile 不允许修改配置。',
      failed: '未能完成保存，已保留输入。可放弃修改以读取当前配置，再重新填写并保存。',
      failedKeyPresent: '未能完成保存。上次检查时，上方地址已配置密钥。输入已保留；可放弃修改以读取最新配置。',
      failedKeyMissing: '未能完成保存。上次检查时，上方地址未配置密钥。输入已保留；可放弃修改以读取最新配置。',
      failedKeyUnknown: '未能完成保存，也无法检查密钥状态。输入已保留；请放弃修改以读取最新配置，并在重试前检查密钥。',
      missingKey: '请先填写此地址的 API key；不会复用其他地址的密钥。',
      invalidUrl: '请填写 HTTPS 基础地址，不包含账号密码、查询参数、片段或 /api/v1。',
      invalidKey: 'API key 不能包含换行。',
      secureContext: '请通过 HTTPS 或 localhost 打开此 dsh 页面，再配置凭据。',
      legacyHttp: '已禁止 HTTP 请求。请改为 HTTPS 地址；仍可清除旧 HTTP 地址对应的已存密钥。',
      storage: '密钥保存在 dsh 原生凭据库中，默认是权限受限的本地文件，并非系统加密钥匙串；同一系统账号下的进程仍可能读取。',
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

    function canonicalAddress(value, allowHttp = false) {
      const trimmed = value.trim();
      const address = new URL(trimmed);
      if (!/^https?:\/\//i.test(trimmed) ||
        (address.protocol !== 'https:' && !(allowHttp && address.protocol === 'http:')) ||
        address.username || address.password || address.href.includes('?') || address.href.includes('#') ||
        /\/api\/v1$/i.test(address.pathname.replace(/\/+$/, ''))) throw new Error('Invalid URL');
      return address.toString().replace(/\/+$/, '');
    }
    function secureUi() {
      return globalThis.isSecureContext !== false && typeof globalThis.crypto?.subtle?.digest === 'function';
    }
    async function credentialReference(address) {
      if (!secureUi()) throw new Error('Secure UI required');
      const hash = await globalThis.crypto.subtle.digest('SHA-256', new TextEncoder().encode(address));
      return `TALUS_${Array.from(new Uint8Array(hash), byte => byte.toString(16).padStart(2, '0')).join('').toUpperCase()}`;
    }
    function aborted(signal) {
      if (signal.aborted) throw new DOMException('Operation cancelled', 'AbortError');
    }

    function TalusPanel({ talusForm, talusDescription, credentials, t }) {
      const subscribe = React.useCallback(listener => talusForm.subscribe(listener), [talusForm]);
      const getSnapshot = React.useCallback(() => talusForm.getSnapshot(), [talusForm]);
      const snapshot = React.useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
      const subscribeDescription = React.useCallback(listener => talusDescription.subscribe(listener), [talusDescription]);
      const getDescription = React.useCallback(() => talusDescription.getSnapshot(), [talusDescription]);
      const description = React.useSyncExternalStore(subscribeDescription, getDescription, getDescription);
      const legacyKey = description.view?.namespaces?.find(row => row.ns === 'talus')?.secrets?.some(
        slot => slot.set && slot.path.length === 1 && slot.path[0] === 'apiKey');
      const [url, setUrl] = React.useState('');
      const [apiKey, setApiKey] = React.useState('');
      const [clearKey, setClearKey] = React.useState(false);
      const [dirty, setDirty] = React.useState(false);
      const [revision, setRevision] = React.useState();
      const [saving, setSaving] = React.useState(false);
      const [message, setMessage] = React.useState('');
      const [credential, setCredential] = React.useState({ status: 'invalid' });
      const [credentialRevision, setCredentialRevision] = React.useState(0);
      const lifetime = React.useRef();
      const savingNow = React.useRef(false);
      React.useEffect(() => {
        const controller = new AbortController();
        lifetime.current = controller;
        return () => { controller.abort(); };
      }, []);
      React.useEffect(() => {
        if (!dirty) {
          setUrl(typeof snapshot.value?.url === 'string' ? snapshot.value.url : '');
          setRevision(snapshot.revision);
        }
      }, [snapshot, dirty]);
      React.useEffect(() => {
        const controller = new AbortController();
        let address;
        try { address = canonicalAddress(url, true); }
        catch { setCredential({ status: 'invalid' }); return () => controller.abort(); }
        if (!secureUi()) {
          setCredential({ status: 'insecure', address });
          return () => controller.abort();
        }
        setCredential({ status: 'loading', address });
        const timer = setTimeout(async () => {
          try {
            const ref = await credentialReference(address);
            aborted(controller.signal);
            const view = await credentials.describe(ref, controller.signal);
            aborted(controller.signal);
            setCredential({ status: 'ready', address, ref, ...view });
          } catch {
            if (!controller.signal.aborted) setCredential({ status: 'failed', address });
          }
        }, 180);
        return () => { clearTimeout(timer); controller.abort(); };
      }, [url, credentialRevision, credentials]);
      const editable = snapshot.status === 'ready' && snapshot.writable && !saving;
      const canEditKey = editable && secureUi() && credential.writable !== false;
      let legacyHttp = false;
      try { legacyHttp = new URL(url).protocol === 'http:'; } catch {}
      function edit() {
        if (!dirty) setRevision(snapshot.revision);
        setDirty(true);
        setMessage('');
      }
      function reset() {
        setUrl(typeof snapshot.value?.url === 'string' ? snapshot.value.url : '');
        setApiKey(''); setClearKey(false); setDirty(false); setMessage('');
        setRevision(snapshot.revision);
        setCredentialRevision(value => value + 1);
      }
      async function save(event) {
        event.preventDefault();
        if (!editable || !dirty || savingNow.current) return;
        let address;
        try {
          address = canonicalAddress(url, clearKey && !apiKey.trim());
          if (address.startsWith('http:') && address !== canonicalAddress(snapshot.value?.url ?? '', true)) throw new Error('Invalid URL');
        } catch { setMessage('invalidUrl'); return; }
        if (/[\r\n]/.test(apiKey)) { setMessage('invalidKey'); return; }
        if (!secureUi()) { setMessage('secureContext'); return; }
        const signal = lifetime.current.signal;
        const key = apiKey.trim();
        savingNow.current = true;
        setSaving(true); setMessage('');
        let ref;
        // Credentials and public settings use separate stores. If either call
        // fails, inspect the target again: even a rejected write may have
        // committed before its response was lost.
        async function reportFailure() {
          if (signal.aborted) return;
          if (!ref) { setMessage('failed'); return; }
          try {
            const view = await credentials.describe(ref, signal);
            aborted(signal);
            setCredential({ status: 'ready', address, ref, ...view });
            setMessage(view.configured ? 'failedKeyPresent' : 'failedKeyMissing');
          } catch {
            if (!signal.aborted) setMessage('failedKeyUnknown');
          }
        }
        try {
          ref = await credentialReference(address);
          aborted(signal);
          const current = await credentials.describe(ref, signal);
          if (key || clearKey) {
            if (!current.writable) { setMessage('keyReadOnly'); return; }
            if (key) await credentials.set(ref, key, signal);
            else await credentials.unset(ref, signal);
          } else if (!current.configured) {
            setMessage('missingKey');
            return;
          }
          aborted(signal);
          let savedAddress;
          try { savedAddress = canonicalAddress(snapshot.value?.url ?? '', true); } catch {}
          // Native key writes can trigger Host-side legacy cleanup, which advances
          // settings revision. Same-address key changes need no settings write.
          const accepted = address === savedAddress || await talusForm.mutate([
            { op: 'set', path: ['url'], value: address },
          ], revision);
          aborted(signal);
          if (accepted) {
            setApiKey(''); setClearKey(false); setDirty(false);
            setMessage(clearKey && !key ? 'cleared' : 'saved');
          } else await reportFailure();
        } catch {
          await reportFailure();
        } finally {
          savingNow.current = false;
          if (!signal.aborted) {
            setSaving(false);
            setCredentialRevision(value => value + 1);
          }
        }
      }
      if (snapshot.status === 'loading') return h('p', null, t('loading'));
      if (snapshot.status !== 'ready') return h('p', { role: 'status' }, t('unavailable'));
      const credentialMessage = credential.status === 'loading' ? 'keyChecking'
        : credential.status === 'ready' ? (credential.configured ? 'keyConfigured' : 'keyMissing')
          : credential.status === 'insecure' ? 'secureContext'
            : credential.status === 'failed' ? 'keyUnavailable' : null;
      return h('form', { onSubmit: save, style: { maxWidth: 640, display: 'grid', gap: 16, padding: '16px 0' } },
        h('label', { style: { display: 'grid', gap: 8 } },
          h('span', { style: { fontWeight: 600 } }, t('url')),
          h('input', {
            type: 'url', name: 'talus-url', value: url, placeholder: 'https://talus.example.com',
            disabled: !editable, autoComplete: 'url', spellCheck: false, style: fieldStyle,
            onChange: event => { edit(); setUrl(event.target.value); setApiKey(''); setClearKey(false); },
          }),
          h('small', { style: { opacity: 0.7 } }, t('urlHint'))),
        legacyHttp ? h('p', { role: 'status' }, t('legacyHttp')) : null,
        legacyKey && !credential.configured ? h('p', { role: 'status' }, t('legacyKey')) : null,
        h('label', { style: { display: 'grid', gap: 8 } },
          h('span', { style: { fontWeight: 600 } }, t('key')),
          h('input', {
            type: 'password', name: 'talus-api-key', value: apiKey, autoComplete: 'new-password',
            disabled: !canEditKey, spellCheck: false, style: fieldStyle,
            onChange: event => { edit(); setApiKey(event.target.value); setClearKey(false); },
          }),
          h('small', { style: { opacity: 0.7 } }, t('keyHint'))),
        credentialMessage ? h('p', { role: 'status' }, t(credentialMessage)) : null,
        credential.writable === false ? h('p', { role: 'status' }, t('keyReadOnly')) : null,
        clearKey ? h('p', { role: 'status' }, t('clearPending')) : null,
        message ? h('p', { role: message === 'saved' || message === 'cleared' ? 'status' : 'alert' }, t(message)) : null,
        !snapshot.writable ? h('p', { role: 'status' }, t('readOnly')) : null,
        h('div', { style: { display: 'flex', flexWrap: 'wrap', gap: 8 } },
          h('button', {
            type: 'submit', disabled: !editable || !dirty || !secureUi(),
            style: { ...buttonStyle, background: '#2563eb', color: '#fff', opacity: editable && dirty && secureUi() ? 1 : 0.5 },
          }, t(saving ? 'saving' : 'save')),
          h('button', {
            type: 'button', disabled: !canEditKey,
            style: buttonStyle, onClick: () => { edit(); setApiKey(''); setClearKey(true); },
          }, t('clear')),
          h('button', { type: 'button', disabled: !dirty || saving, onClick: reset, style: buttonStyle }, t('reload'))),
        h('small', { style: { opacity: 0.65 } }, t('storage')));
    }

    return {
      name: 'talus-ui',
      inject: ['slots', 'configForms', 'locale', 'remote', 'remote.credentials'],
      apply(ctx) {
        const lifetime = new AbortController();
        ctx.effect(() => () => lifetime.abort());
        // DSH 0.2.2's credential methods return Replies and do not accept a
        // caller signal. Cancel local follow-up work and discard stale Replies.
        async function invoke(method, args, signal) {
          aborted(lifetime.signal); aborted(signal);
          const response = await ctx.remote.credentials[method](...args);
          aborted(lifetime.signal); aborted(signal);
          if (!response?.ok) throw new Error('Credential operation rejected');
          return response.value;
        }
        const credentials = {
          async describe(ref, signal) {
            const views = await invoke('describe', [[ref]], signal);
            const view = views?.[ref];
            if (typeof view?.configured !== 'boolean' || typeof view?.writable !== 'boolean') throw new Error('Invalid credential status');
            return { configured: view.configured, writable: view.writable };
          },
          set: (ref, key, signal) => invoke('set', [ref, key], signal),
          unset: (ref, signal) => invoke('unset', [ref], signal),
        };
        ctx.effect(() => ctx.locale.register('talus', { en, zh }));
        ctx.slots.inject('plugins.bundle.config', () => ctx.slots.register({
          name: 'plugins.bundle.config', key: 'dsh-plugin-talus', locale: 'talus',
          inject: () => ({
            talusForm: ctx.configForms.get('talus'),
            talusDescription: ctx.configForms.describe(),
            credentials,
          }),
        }, TalusPanel));
      },
    };
  },
});
