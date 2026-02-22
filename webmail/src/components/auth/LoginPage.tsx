import { useState, type FormEvent } from 'react';
import { Eye, EyeOff } from 'lucide-react';
import { useAuthStore } from '@/stores/authStore';

export function LoginPage() {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const { login, isLoading, error } = useAuthStore();

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    await login(email, password);
  };

  return (
    <div className="h-full flex items-center justify-center bg-background">
      <div className="w-[340px] rounded-2xl bg-card p-6 space-y-5">
        {/* Header */}
        <div className="flex flex-col items-center gap-2">
          {/* Icon */}
          <div className="w-10 h-10 rounded-2xl bg-secondary flex items-center justify-center text-lg">
            ✉
          </div>
          {/* Title */}
          <h1 className="font-heading-oswald text-2xl font-bold tracking-wider text-foreground">
            REST MAIL
          </h1>
          {/* Subtitle */}
          <p className="font-mono text-[11px] text-muted-foreground">
            // sign_in_to_your_account
          </p>
        </div>

        {/* Form */}
        <form onSubmit={handleSubmit} className="space-y-3">
          {/* Email field */}
          <div className="space-y-1">
            <label className="font-mono text-xs font-medium text-muted-foreground">
              email
            </label>
            <div className="flex items-center h-9 rounded-2xl bg-secondary px-3">
              <input
                type="email"
                value={email}
                onChange={e => setEmail(e.target.value)}
                placeholder="alice@restmail.dev"
                autoFocus
                required
                className="flex-1 bg-transparent font-mono text-xs text-foreground placeholder:text-muted-foreground outline-none min-w-0"
              />
            </div>
          </div>

          {/* Password field */}
          <div className="space-y-1">
            <label className="font-mono text-xs font-medium text-muted-foreground">
              password
            </label>
            <div className="flex items-center h-9 rounded-2xl bg-secondary px-3">
              <input
                type={showPassword ? 'text' : 'password'}
                value={password}
                onChange={e => setPassword(e.target.value)}
                placeholder="••••••••"
                required
                className="flex-1 bg-transparent font-mono text-xs text-foreground placeholder:text-muted-foreground outline-none min-w-0"
              />
              <button
                type="button"
                onClick={() => setShowPassword(!showPassword)}
                className="text-muted-foreground hover:text-foreground shrink-0 ml-2"
              >
                {showPassword ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
              </button>
            </div>
          </div>

          {/* Error */}
          {error && (
            <p className="font-mono text-xs text-destructive">// error: {error}</p>
          )}

          {/* Sign in button */}
          <button
            type="submit"
            disabled={isLoading}
            className="w-full h-10 rounded-2xl bg-primary text-primary-foreground font-mono text-xs font-semibold hover:opacity-90 transition-opacity disabled:opacity-50"
          >
            {isLoading ? '// signing_in...' : 'sign_in'}
          </button>
        </form>
      </div>
    </div>
  );
}
