import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { api } from "./api";

export type UserRole = "ADMIN" | "USER";
export type UserStatus = "PENDING" | "APPROVED" | "REJECTED" | "DISABLED";

export interface User {
  id: number;
  username: string;
  email: string;
  role: UserRole;
  status: UserStatus;
  created_at: string;
  updated_at: string;
}

interface AuthState {
  user: User | null;
  loading: boolean;
  refresh: () => Promise<void>;
  signIn: (username: string, password: string) => Promise<void>;
  signOut: () => Promise<void>;
}

const AuthContext = createContext<AuthState | undefined>(undefined);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  async function refresh() {
    try {
      const res = await api.get<{ user: User }>("/api/auth/me");
      setUser(res.user);
    } catch (e) {
      // 401 → not signed in, which is normal on first load.
      if ((e as { status?: number }).status === 401) {
        setUser(null);
      } else {
        // Network / 5xx: leave user null but log so dev can see.
        // eslint-disable-next-line no-console
        console.error("auth refresh failed", e);
        setUser(null);
      }
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
  }, []);

  async function signIn(username: string, password: string) {
    const res = await api.post<{ user: User }>("/api/auth/login", {
      username,
      password,
    });
    setUser(res.user);
  }

  async function signOut() {
    await api.post("/api/auth/logout");
    setUser(null);
  }

  return (
    <AuthContext.Provider value={{ user, loading, refresh, signIn, signOut }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within <AuthProvider>");
  return ctx;
}
