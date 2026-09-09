// Use in the Electron main process. Keep authentication tokens out of logs.
export function createCatalogClient(baseUrl, getAccessToken = async () => null) {
  const base = baseUrl.replace(/\/$/, '');
  async function request(path, method = 'GET', body) {
    const headers = {};
    if (method !== 'GET') {
      const token = await getAccessToken();
      if (!token) throw new Error('Sign in before submitting or voting.');
      headers.Authorization = `Bearer ${token}`;
      headers['Content-Type'] = 'application/json';
    }
    const response = await fetch(`${base}${path}`, {
      method, headers, body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(20000),
    });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error?.message ?? `HTTP ${response.status}`);
    return data;
  }
  function collection(kind) {
    return {
      create: data => request(`/v1/${kind}`, 'POST', data),
      get: id => request(`/v1/${kind}/${encodeURIComponent(id)}`),
      vote: (id, value) => request(`/v1/${kind}/${encodeURIComponent(id)}/vote`, 'PUT', { value }),
      async listAll() {
        const items = [];
        let cursor;
        do {
          const query = new URLSearchParams({ limit: '50' });
          if (cursor) query.set('cursor', cursor);
          const page = await request(`/v1/${kind}?${query}`);
          items.push(...page.items);
          cursor = page.nextCursor;
        } while (cursor);
        return items;
      },
    };
  }
  return { options: collection('options'), presets: collection('presets') };
}
