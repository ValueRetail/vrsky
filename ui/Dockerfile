# Multi-stage build for React + TypeScript SPA
# Stage 1: Build
FROM node:20-alpine AS builder

WORKDIR /app

# Copy package files
COPY ui/package*.json ./
COPY ui/tsconfig*.json ./
COPY ui/vite.config.ts ./
COPY ui/postcss.config.js ./
COPY ui/tailwind.config.js ./
COPY ui/eslint.config.js ./

# Install dependencies
RUN npm ci

# Copy source code
COPY ui/src ./src
COPY ui/public ./public
COPY ui/index.html ./
COPY ui/tests ./tests
COPY ui/vitest.config.ts ./

# Build application
RUN npm run build

# Stage 2: Serve with Nginx
FROM nginx:1.27-alpine AS nginx

# Install curl for health checks
RUN apk add --no-cache curl

# Copy nginx configuration
RUN rm -f /etc/nginx/conf.d/default.conf
COPY ui/nginx.conf /etc/nginx/nginx.conf
COPY ui/nginx.default.conf /etc/nginx/conf.d/default.conf

# Copy built application from builder stage
COPY --from=builder /app/dist /usr/share/nginx/html

# Set ownership and permissions
RUN chown -R nginx:nginx /usr/share/nginx/html && \
    chown -R nginx:nginx /var/cache/nginx && \
    chown -R nginx:nginx /var/log/nginx && \
    chmod -R 755 /usr/share/nginx/html

# Switch to non-root user
USER nginx

# Expose port
EXPOSE 80

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD curl -f http://localhost/index.html || exit 1

# Start nginx
CMD ["nginx", "-g", "daemon off;"]
