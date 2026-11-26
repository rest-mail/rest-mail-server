import type { Plugin } from 'vite';

export function devMock(): Plugin {
  const messages = Array.from({ length: 56 }, (_, index) => ({
    id: index + 1, mailbox_id: 1, thread_id: index < 2 ? 'welcome' : `thread-${index}`,
    class: 'personal', folder: 'INBOX', message_id: `<fixture-${index}@example.test>`,
    sender: index === 0 ? 'team@restmail.test' : 'alex@example.test', sender_name: index === 0 ? 'REST Mail team' : 'Alex Morgan',
    recipients_to: ['chris@restmail.test'], subject: index === 0 ? 'Welcome to REST Mail desktop' : index === 1 ? 'Re: Welcome to REST Mail desktop' : ['Weekend plans', 'Project notes', 'Design review'][index % 3] + ` ${index + 1}`,
    size_bytes: 2048, has_attachments: index === 0, is_read: index > 3, is_flagged: false, is_starred: false, is_draft: false,
    received_at: new Date(Date.UTC(2026, 10, 21, 12, 0) - index * 3600000).toISOString(),
    body_text: 'Your mail, in one place. This is development fixture data, never included in a production build.',
    body_html: '<h1>Welcome aboard</h1><p>Your mail, in one place.</p><p>This is the shared REST Mail reader, running on your desktop.</p><img src="https://example.invalid/tracking.png" alt="Remote image blocked"><script>window.__mailScriptRan = true</script>',
    headers: { From: 'team@restmail.test', Subject: 'Welcome to REST Mail desktop' }, in_reply_to: '', references: '',
  }));
  return {
    name: 'desktop-development-fixtures', apply: 'serve',
    configureServer(server) {
      server.middlewares.use(async (request, response, next) => {
        const url = new URL(request.url ?? '/', 'http://localhost');
        url.pathname = url.pathname.replace(/^\/__desktop_mock(?=\/api\/)/, '');
        if (!url.pathname.startsWith('/api/')) return next();
        const route = url.pathname.slice('/api/v1'.length);
        let body = '';
        for await (const chunk of request) body += chunk;
        const input = body ? JSON.parse(body) : {};
        let result: unknown = { data: {} };
        if (route.endsWith('/events')) { response.writeHead(200, { 'Content-Type': 'text/event-stream' }); response.write(': fixture stream\n\n'); return; }
        if (route === '/accounts') result = { data: [{ id: 1, address: 'chris@restmail.test', display_name: 'Chris', is_primary: true }] };
        else if (route.endsWith('/folders')) result = { data: ['INBOX', 'Sent', 'Drafts', 'Archive', 'Trash', 'Projects/2026'].map(name => ({ name, total: messages.filter(message => message.folder === name).length, unread: messages.filter(message => message.folder === name && !message.is_read).length })) };
        else if (route.endsWith('/quota')) result = { data: { quota_bytes: 1073741824, quota_used_bytes: 12000000, message_count: messages.length, percent_used: 1.1 } };
        else if (route.endsWith('/search')) result = { data: messages.filter(message => `${message.subject} ${message.body_text}`.toLowerCase().includes((url.searchParams.get('q') ?? '').toLowerCase())) };
        else if (/\/folders\/.+\/messages$/.test(route)) {
          const folder = decodeURIComponent(route.split('/folders/')[1].split('/messages')[0]);
          const start = Number(url.searchParams.get('cursor') ?? 0);
          const folderMessages = messages.filter(message => message.folder === folder);
          result = { data: folderMessages.slice(start, start + 50), pagination: { cursor: String(start + 50), has_more: start + 50 < folderMessages.length, total: folderMessages.length } };
        } else if (/\/threads\/.+\/messages$/.test(route)) result = { data: messages.filter(message => message.thread_id === decodeURIComponent(route.split('/threads/')[1].split('/')[0])) };
        else if (route.endsWith('/attachments')) result = { data: [{ id: 1, filename: 'welcome.txt', content_type: 'text/plain', size_bytes: 24 }] };
        else if (route.startsWith('/attachments/')) { response.setHeader('Content-Type', 'text/plain'); response.end('Welcome to REST Mail!\n'); return; }
        else if (route.endsWith('/contacts/suggest')) result = { data: [{ id: 1, name: 'Alex Morgan', email: 'alex@example.test' }] };
        else if (route === '/messages/send' || route.endsWith('/send') || route.endsWith('/forward')) result = { data: { id: 99 } };
        else if (route === '/messages/draft' || route.startsWith('/messages/draft/')) {
          const id = Number(route.split('/').pop()) || 99;
          let draft = messages.find(message => message.id === id);
          if (!draft) { draft = { ...messages[0], id, folder: 'Drafts', is_draft: true }; messages.push(draft); }
          Object.assign(draft, input); result = { data: draft };
        } else if (/^\/messages\/\d+$/.test(route)) {
          const message = messages.find(item => item.id === Number(route.split('/').pop()));
          if (request.method === 'PATCH') Object.assign(message ?? {}, input);
          if (request.method === 'DELETE' && message) message.folder = 'Trash';
          result = { data: message };
        } else if (route.endsWith('/recipients') || route.endsWith('/scheduled') || route.endsWith('/awaiting')) result = { data: [] };
        else if (route.endsWith('/seal')) result = { data: { valid: true, sealed: false } };
        else if (route.endsWith('/time-zone')) result = { data: { time_zone: 'Europe/Berlin' } };
        response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify(result));
      });
    },
  };
}
