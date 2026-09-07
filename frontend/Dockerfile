# syntax=docker/dockerfile:1.7
#
# Podium frontend — multi-stage build.
#
# Stage 1 (build): node:20-bookworm compiles the React + Vite bundle to
#                  frontend/dist/. The Vite build is a plain
#                  `npm run build` (tsc -b && vite build).
#
# Stage 2 (runtime): nginx:1.27-alpine serves the bundle. We need
#                    nginx (not a static file server) because the SPA
#                    fallback has to rewrite unknown paths to
#                    index.html, and we need a proxy_pass for /api and
#                    /healthz to the Go backend container. alpine keeps
#                    the runtime image ~12MB.
#
# Build context must be the frontend/ subdir; backend/ is not needed
# here. See frontend/.dockerignore for the exclusion list.

FROM node:20-bookworm AS build
WORKDIR /src

# Cache the npm install layer separately so source edits don't bust
# the cache. package.json + package-lock.json are the only files needed
# for npm ci.
COPY package.json package-lock.json ./
RUN npm ci

COPY . .
RUN npm run build   # tsc -b && vite build, emits dist/

FROM nginx:1.27-alpine
# Drop the default server block that ships in the base image.
RUN rm /etc/nginx/conf.d/default.conf
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist /usr/share/nginx/html

EXPOSE 80

# nginx:alpine's CMD is already `nginx -g 'daemon off;'`. We just
# override the working directory.
WORKDIR /usr/share/nginx/html
