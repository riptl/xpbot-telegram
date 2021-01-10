FROM node:15-alpine
RUN addgroup -S app \
 && adduser -S app -G app
WORKDIR /app
COPY package.json ./ 
RUN npm i --production
COPY index.js ./
RUN chown app:app /app
USER app
CMD ["index.js"]
