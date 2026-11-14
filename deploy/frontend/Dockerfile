# Build del frontend (contexto: ../frontend) + nginx con proxy same-origin al backend.
# nginx.conf viene del contexto adicional "deploy" (deploy/frontend) declarado en docker-compose.yml.
FROM node:22-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

FROM nginx:1.28-alpine
COPY --from=deploy nginx.conf /etc/nginx/nginx.conf
COPY --from=build /src/dist /usr/share/nginx/html
USER nginx
EXPOSE 8080
