import { betterAuth } from "better-auth";
import { jwt } from "better-auth/plugins";
import Database from "better-sqlite3";
import path from "path";
import fs from "fs";

// Ensure data directory exists
const dataDir = path.join(process.cwd(), "data");
if (!fs.existsSync(dataDir)) {
  fs.mkdirSync(dataDir, { recursive: true });
}

const dbPath = process.env.DATABASE_PATH || path.join(dataDir, "auth.db");

// Optimize SQLite for low latency
const db = new Database(dbPath);
db.pragma("journal_mode = WAL"); // Write-Ahead Logging for better concurrency
db.pragma("synchronous = NORMAL"); // Balance between safety and speed
db.pragma("cache_size = 10000"); // Increase cache size for faster queries
db.pragma("temp_store = MEMORY"); // Store temp tables in memory

export const auth = betterAuth({
  database: db,
  secret: process.env.BETTER_AUTH_SECRET || "secret-key-change-in-production",
  baseURL: process.env.BETTER_AUTH_URL || "http://localhost:3000",
  emailAndPassword: {
    enabled: true,
    // Disable email verification for fastest signup
    requireEmailVerification: false,
    // Auto-signin after signup for better UX
    autoSignIn: true,
  },
  plugins: [
    jwt({
      // Short-lived tokens for security, but long enough to avoid frequent renewal
      expiresIn: 60 * 60 * 2, // 2 hours (optimized balance)
      // Use RS256 for asymmetric signing (more secure for distributed systems)
      algorithm: "RS256",
    } as unknown as any),
  ],
  session: {
    // Longer session for fewer auth checks
    expiresIn: 60 * 60 * 24 * 30, // 30 days
    updateAge: 60 * 60 * 24, // Update session daily
    cookieCache: {
      enabled: true,
      maxAge: 60 * 5, // 5 minutes cache
    },
  },
  // Advanced configuration for performance
  advanced: {
    useSecureCookies: process.env.NODE_ENV === "production",
    cookiePrefix: "better-auth",
    // Optimize database queries
    disableCSRFCheck: process.env.NODE_ENV === "development",
  },
});

export type Session = typeof auth.$Infer.Session;
