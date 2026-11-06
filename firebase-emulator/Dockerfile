FROM node:26-slim@sha256:5adad64f9a1fca2d51972caa65983634c8a774eb300d72b20f7f2a0f60368072

RUN apt-get update \
 && apt-get install -y --no-install-recommends wget gnupg ca-certificates \
 && mkdir -p /etc/apt/keyrings \
 && wget -qO - https://packages.adoptium.net/artifactory/api/gpg/key/public \
      | gpg --dearmor -o /etc/apt/keyrings/adoptium.gpg \
 && echo "deb [signed-by=/etc/apt/keyrings/adoptium.gpg] https://packages.adoptium.net/artifactory/deb $(. /etc/os-release && echo $VERSION_CODENAME) main" \
      > /etc/apt/sources.list.d/adoptium.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends temurin-21-jre \
 && rm -rf /var/lib/apt/lists/*

RUN npm install -g firebase-tools

WORKDIR /srv

RUN firebase setup:emulators:firestore \
 && firebase setup:emulators:ui

COPY firebase.json firestore.rules ./

EXPOSE 4000 4400 4500 8080 9099

CMD ["firebase", "emulators:start", "--project", "demo-tolo"]
