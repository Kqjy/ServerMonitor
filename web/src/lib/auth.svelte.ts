import { api, ApiError, type Me } from '$lib/api';
import { goto } from '$app/navigation';

type Status = 'unknown' | 'needs-setup' | 'guest' | 'authed';

class AuthState {
  user = $state<Me | null>(null);
  status = $state<Status>('unknown');

  async refresh(): Promise<Status> {
    try {
      const s = await api.authStatus();
      if (!s.initialized) {
        this.user = null;
        this.status = 'needs-setup';
        return this.status;
      }
    } catch {
      this.status = 'guest';
      return this.status;
    }
    try {
      const me = await api.me();
      this.user = me;
      this.status = 'authed';
    } catch (e) {
      this.user = null;
      this.status = e instanceof ApiError && e.status === 401 ? 'guest' : 'guest';
    }
    return this.status;
  }

  async login(username: string, password: string) {
    const me = await api.login(username, password);
    this.user = me;
    this.status = 'authed';
  }

  async setup(username: string, password: string) {
    const me = await api.setup(username, password);
    this.user = me;
    this.status = 'authed';
  }

  async logout() {
    try {
      await api.logout();
    } catch {}
    this.user = null;
    this.status = 'guest';
    await goto('/login');
  }
}

export const auth = new AuthState();
