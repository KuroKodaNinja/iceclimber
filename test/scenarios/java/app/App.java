import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import java.nio.file.Files;
import java.nio.file.Paths;

// App parses the comic JSON Popo fetched (args[0]) using Gson — a real Maven
// dependency resolved onto the classpath — and prints a computed one-line report.
public class App {
    public static void main(String[] args) throws Exception {
        String json = new String(Files.readAllBytes(Paths.get(args[0])));
        JsonObject o = JsonParser.parseString(json).getAsJsonObject();
        int num = o.get("num").getAsInt();
        String title = o.get("title").getAsString();
        System.out.println("comic #" + num + " : " + title + " (len " + title.length() + ")");
    }
}
