FROM python:3.12-slim
WORKDIR /app
COPY --chmod=0644 requirements.txt .
RUN python -m pip install --no-cache-dir -r requirements.txt
COPY --chmod=0644 *.py .
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
USER 1000:1000
EXPOSE 8001
ENTRYPOINT ["python", "-m", "uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8001", "--no-access-log"]
