import express, { Request, Response } from "express";
import cors from "cors";
import { auth } from "./lib/auth";
import dotenv from "dotenv";
import compression from "compression";
import jwt from "jsonwebtoken";
import crypto from "crypto";
import fs from "fs";
import path from "path";

dotenv.config();

const app = express();
const PORT = process.env.PORT || 3000;

// Generate or load RSA key pair for JWT signing
let privateKey: string;
let publicKey: string;

const keyPath = path.join(process.cwd(), "data", "jwt-keys.json");

try {
  // Try to load existing keys
  if (fs.existsSync(keyPath)) {
    const keys = JSON.parse(fs.readFileSync(keyPath, "utf-8"));
    privateKey = keys.privateKey;
    publicKey = keys.publicKey;
    console.log("✓ Loaded existing JWT keys");
  } else {
    // Generate new keys
    const { privateKey: priv, publicKey: pub } = crypto.generateKeyPairSync(
      "rsa",
      {
        modulusLength: 2048,
        publicKeyEncoding: { type: "spki", format: "pem" },
        privateKeyEncoding: { type: "pkcs8", format: "pem" },
      }
    );
    privateKey = priv;
    publicKey = pub;

    // Save keys
    fs.writeFileSync(keyPath, JSON.stringify({ privateKey, publicKey }));
    console.log("✓ Generated and saved new JWT keys");
  }
} catch (error) {
  console.error("Error handling RSA keys:", error);
  process.exit(1);
}

// Performance optimizations
app.disable("x-powered-by"); // Remove unnecessary header
app.set("trust proxy", 1); // Trust first proxy for better performance

// Middleware - optimized order for speed
app.use(compression()); // Compress responses
app.use(
  cors({
    origin: process.env.CORS_ORIGIN || "*", // In production, specify your frontend URL
    credentials: true,
    maxAge: 86400, // Cache preflight requests for 24 hours
  })
);
app.use(express.json({ limit: "10kb" })); // Limit payload size for security and speed

// JWKS endpoint - provides public key for JWT verification
app.get("/api/auth/jwks", (req: Request, res: Response) => {
  try {
    // Convert PEM public key to JWK format
    const keyObject = crypto.createPublicKey(publicKey);
    const jwk = keyObject.export({ format: "jwk" }) as any;

    // Ensure all required fields are present
    const jwkWithMetadata = {
      kty: jwk.kty,
      n: jwk.n,
      e: jwk.e,
      kid: "main-key",
      use: "sig",
      alg: "RS256",
    };

    res.json({
      keys: [jwkWithMetadata],
    });
  } catch (error) {
    console.error("Error generating JWKS:", error);
    res.status(500).json({ error: "Failed to generate JWKS" });
  }
});

// Health check endpoint - optimized for monitoring
app.get("/health", (req: Request, res: Response) => {
  res.status(200).json({
    status: "ok",
    service: "better-auth",
    timestamp: Date.now(),
    uptime: process.uptime(),
  });
});

// Test OAuth flow page
app.get("/test-oauth", (req: Request, res: Response) => {
  res.send(`
    <!DOCTYPE html>
    <html>
    <head><title>Test OAuth</title></head>
    <body>
      <h1>Test Google OAuth</h1>
      <button onclick="startOAuth()">Sign in with Google</button>
      <div id="result" style="margin-top: 20px;"></div>
      <script>
        async function startOAuth() {
          const response = await fetch('/api/auth/sign-in/social', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({provider: 'google', callbackURL: '/test-oauth-success'})
          });
          const data = await response.json();
          if (data.url) {
            window.location.href = data.url;
          }
        }
      </script>
    </body>
    </html>
  `);
});

// OAuth success page - shows JWT token
app.get("/test-oauth-success", async (req: Request, res: Response) => {
  const cookies = req.headers.cookie || "";
  const sessionToken = cookies
    .split(";")
    .find((c) => c.trim().startsWith("better-auth.session_token="));

  if (!sessionToken) {
    return res.send("<h1>No session found</h1><p>OAuth might have failed.</p>");
  }

  // Get session to find user
  const token = sessionToken.split("=")[1];

  res.send(`
    <!DOCTYPE html>
    <html>
    <head><title>OAuth Success</title></head>
    <body>
      <h1>✅ OAuth Successful!</h1>
      <p>Session token: <code>${token}</code></p>
      <button onclick="getJWT()">Get JWT Token</button>
      <pre id="jwt" style="background: #f0f0f0; padding: 10px; margin-top: 10px;"></pre>
      <script>
        async function getJWT() {
          const response = await fetch('/api/auth/get-session', {
            credentials: 'include'
          });
          const data = await response.json();
          document.getElementById('jwt').textContent = JSON.stringify(data, null, 2);
        }
      </script>
    </body>
    </html>
  `);
});

// Test endpoint to generate JWT for a user
app.get("/test-jwt/:userId", (req: Request, res: Response) => {
  const userId = req.params.userId;

  const jwtToken = jwt.sign(
    { sub: userId, email: "vaibhav.martian@gmail.com" },
    privateKey,
    { algorithm: "RS256", expiresIn: "2h", keyid: "main-key" }
  );

  res.json({ userId, jwt: jwtToken });
});

// OAuth token endpoint - fetch tokens for connected accounts
app.get(
  "/api/auth/accounts/:provider/token",
  async (req: Request, res: Response) => {
    try {
      const authHeader = req.headers.authorization;
      if (!authHeader?.startsWith("Bearer ")) {
        return res.status(401).json({ error: "Missing authorization" });
      }

      const token = authHeader.substring(7);

      // Verify JWT and extract user ID
      let userId: string;
      try {
        const decoded = jwt.verify(token, publicKey, {
          algorithms: ["RS256"],
        }) as any;
        userId = decoded.sub;
      } catch (error) {
        return res.status(401).json({ error: "Invalid token" });
      }

      const provider = req.params.provider;

      // Query database directly with better-sqlite3
      const db = (auth as any).options.database;
      const account = db
        .prepare("SELECT * FROM account WHERE userId = ? AND providerId = ?")
        .get(userId, provider);

      if (!account) {
        return res
          .status(404)
          .json({ error: `No ${provider} account connected` });
      }

      // Convert expires_at to Unix timestamp if it's a date string
      let expiresAt = 0;
      if (account.accessTokenExpiresAt) {
        if (typeof account.accessTokenExpiresAt === "string") {
          expiresAt = Math.floor(
            new Date(account.accessTokenExpiresAt).getTime() / 1000
          );
        } else {
          expiresAt = account.accessTokenExpiresAt;
        }
      }

      res.json({
        access_token: account.accessToken,
        refresh_token: account.refreshToken,
        expires_at: expiresAt,
      });
    } catch (error) {
      console.error("Error fetching account token:", error);
      res.status(500).json({ error: "Failed to fetch token" });
    }
  }
);

// Readiness probe - checks database connection
app.get("/ready", async (req: Request, res: Response) => {
  try {
    // Quick database check (this validates the auth instance)
    const testQuery = await auth.api.listSessions({ headers: req.headers });
    res.status(200).json({ status: "ready" });
  } catch (error) {
    res
      .status(503)
      .json({ status: "not ready", error: "Database unavailable" });
  }
});

// Better Auth handler - handles all /api/auth/* routes
app.all("/api/auth/*", async (req: Request, res: Response) => {
  console.log(`[BetterAuth] ${req.method} ${req.url}`);

  // Better Auth expects a Web Request object
  // We need to convert Express request to a proper Request
  const url = new URL(req.url, `${req.protocol}://${req.get("host")}`);

  // Use globalThis.Request to avoid conflict with Express Request type
  const webRequest = new globalThis.Request(url, {
    method: req.method,
    headers: new Headers(req.headers as Record<string, string>),
    body:
      req.method !== "GET" && req.method !== "HEAD"
        ? JSON.stringify(req.body)
        : undefined,
  });

  try {
    const response = await auth.handler(webRequest);
    console.log(`[BetterAuth] Response status: ${response.status}`);

    // Check if response has JSON content before parsing
    const contentType = response.headers.get("content-type");
    const hasJsonContent = contentType?.includes("application/json");

    // For redirects or non-JSON responses, just forward them
    if (!hasJsonContent || response.status === 301 || response.status === 302) {
      response.headers.forEach((value, key) => {
        res.setHeader(key, value);
      });

      if (response.body) {
        const reader = response.body.getReader();
        const stream = new ReadableStream({
          async start(controller) {
            while (true) {
              const { done, value } = await reader.read();
              if (done) break;
              controller.enqueue(value);
            }
            controller.close();
          },
        });

        // For redirects, just set status and location
        if (response.status === 301 || response.status === 302) {
          return res.redirect(
            response.status,
            response.headers.get("location") || "/"
          );
        }

        res.status(response.status);
        for await (const chunk of stream as any) {
          res.write(chunk);
        }
        return res.end();
      }

      return res.status(response.status).end();
    }

    // Clone the response to read it
    const responseClone = response.clone();
    const responseData = (await response.json()) as any;

    // Check if this is a sign-in or sign-up endpoint
    const isAuthEndpoint =
      req.path.includes("/sign-in/") || req.path.includes("/sign-up/");

    // If it's an auth endpoint and authentication was successful, add JWT token
    if (isAuthEndpoint && responseData && responseData.user) {
      try {
        // Generate JWT token using our RSA private key
        const jwtToken = jwt.sign(
          {
            sub: responseData.user.id,
            email: responseData.user.email,
            name: responseData.user.name,
          },
          privateKey,
          {
            algorithm: "RS256",
            expiresIn: "2h",
            keyid: "main-key",
          }
        );

        // Add JWT token to response
        responseData.jwt = jwtToken;
      } catch (error) {
        console.error("Error generating JWT:", error);
        // Continue without JWT token
      }
    }

    // Set response headers from original response
    responseClone.headers.forEach((value, key) => {
      res.setHeader(key, value);
    });

    // Send response with potentially added JWT
    res.status(responseClone.status).json(responseData);
  } catch (error) {
    console.error("Error handling auth request:", error);
    res.status(500).json({ error: "Internal server error" });
  }
});

app.listen(PORT, () => {
  console.log(`🚀 Better Auth server running on http://localhost:${PORT}`);
  console.log(
    `📚 Auth endpoints available at http://localhost:${PORT}/api/auth/*`
  );
  console.log(`🔑 JWKS endpoint: http://localhost:${PORT}/api/auth/jwks`);
  console.log(
    `✨ JWT tokens (RS256) automatically included in sign-in/sign-up responses`
  );
});
