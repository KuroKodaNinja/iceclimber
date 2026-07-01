package com.example;

import com.google.gson.Gson;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import java.nio.file.Files;
import java.nio.file.Path;

// A real Maven project built by `mvn -o package` inside the iceclimber sandbox (the
// air-gapped Maven build). It uses its Gson dependency — resolved by the controller and
// relayed into the offline .m2 repo — to parse the xkcd comic fetched through Popo and
// re-serialize a computed summary, then prints the values the scenario asserts.
public class App {
    public static void main(String[] args) throws Exception {
        String body = Files.readString(Path.of(args[0]));
        JsonObject comic = JsonParser.parseString(body).getAsJsonObject();
        int num = comic.get("num").getAsInt();
        String title = comic.get("title").getAsString();

        JsonObject summary = new JsonObject();
        summary.addProperty("num", num);
        summary.addProperty("titleLength", title.length()); // Java String.length() = UTF-16 units

        System.out.println("MAVEN_BUILD_OK " + new Gson().toJson(summary));
        System.out.println("xkcd #" + num + ": " + title);
        System.out.println("title length: " + title.length());
    }
}
