FROM python:3.12-slim
WORKDIR /app
COPY workers/extractor/index-requirements.txt .
RUN pip install --no-cache-dir -r index-requirements.txt
COPY --chmod=a+rX workers/extractor/*.py workers/extractor/
COPY --chmod=0755 bin/ontology-engine /app/ontology-engine
COPY web/dist /app/web/dist
RUN chmod -R a+rX /app/web/dist
ENV PYTHONDONTWRITEBYTECODE=1
USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["/app/ontology-engine"]
