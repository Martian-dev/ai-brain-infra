import express, { Request, Response } from "express";
import cors from "cors";
import { auth } from "./lib/auth";
import dotenv from "dotenv";
import compression from "compression";
import jwt from "jsonwebtoken";
import crypto from "crypto";

dotenv.config();

const app = express();
const PORT = process.env.PORT || 3000;

// Generate or load RSA key pair for JWT signing
let privateKey: string;
let publicKey: string;

try {
  // In production, load from env or file. For dev, generate on startup
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
} catch (error) {
  console.error("Error generating RSA keys:", error);
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
